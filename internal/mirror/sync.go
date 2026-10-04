package mirror

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mtimeTolerance = int64(time.Second) // filesystems differ in timestamp precision
	resumeMinSize  = 16 << 20           // only ask for a resume offset on big files
	deleteBatch    = 200
	metaBatch      = 500
	minDeleteGuard = 20 // the percentage guard ignores tiny deletions
	maxConsecFail  = 25
)

type Options struct {
	Src              string
	Excludes         []string
	Compare          string // "mtime" (default) | "checksum"
	Workers          int
	PropagateDeletes bool
	MaxDeletePct     int  // 0 = no percentage guard
	ForceDelete      bool // bypass the percentage and empty-source guards
	DryRun           bool
	LimitKB          int
	TmpDir           string
	PreserveOwner    bool // uid/gid (needs a root destination agent)
	OwnerByName      bool // map users/groups by name instead of numeric id
	PreserveACLs     bool // ACLs and extended attributes

	Log func(level, msg string)
}

// Result holds the counters of one run.
type Result struct {
	SourceEntries, DestEntries int64
	SourceBytes                int64
	New, Changed, Unchanged    int64
	MetaFixed, DirsCreated     int64
	Links                      int64
	FilesCopied, BytesCopied   int64
	BytesPlanned               int64
	ResumedFiles, ResumedBytes int64
	ToDelete, Deleted          int64
	KeptExtra                  int64
	Errors, SourceErrors       int64
	Vanished, ChangedDuring    int64
	MetaWarnings               int64  // ownership/ACLs that could not be applied
	DeleteBlocked              string // non-empty when deletions were deliberately skipped
}

type opKind int

const (
	opCopy opKind = iota
	opMkdir
	opSymlink
	opMeta
	opVerify
)

type op struct {
	kind opKind
	e    Entry
}

// sourceError marks a failure reading the *source* (e.g. file vanished), as
// opposed to a destination-side failure that merely looks similar.
type sourceError struct{ err error }

func (e *sourceError) Error() string { return e.err.Error() }
func (e *sourceError) Unwrap() error { return e.err }

var errTooManyFailures = errors.New("too many consecutive failures (is the destination full or unreachable?)")

type syncer struct {
	t   Target
	o   Options
	res *Result
	lim *Limiter

	consec int64
	logged int64
	dirsF  *os.File
	dirsW  *bufio.Writer
	delF   *os.File
	delW   *bufio.Writer
	delMu  sync.Mutex
	dirMu  sync.Mutex
}

func (s *syncer) log(level, f string, a ...any) {
	if s.o.Log != nil {
		s.o.Log(level, fmt.Sprintf(f, a...))
	}
}

// Sync mirrors o.Src onto t. It never deletes anything unless the scan of the
// source was complete and the safety guards pass.
func Sync(ctx context.Context, t Target, o Options) (*Result, error) {
	if o.Workers < 1 {
		o.Workers = 4
	}
	if o.Workers > 32 {
		o.Workers = 32
	}
	fi, err := os.Stat(o.Src)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("source %s is not a directory", o.Src)
	}
	if err := t.Info(ctx); err != nil {
		return nil, fmt.Errorf("destination not usable: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s := &syncer{t: t, o: o, res: &Result{}, lim: NewLimiter(o.LimitKB)}
	if s.dirsF, err = os.CreateTemp(o.TmpDir, "dirs-*.ndjson"); err != nil {
		return nil, err
	}
	if s.delF, err = os.CreateTemp(o.TmpDir, "del-*.ndjson"); err != nil {
		return nil, err
	}
	defer func() { os.Remove(s.dirsF.Name()); os.Remove(s.delF.Name()); s.dirsF.Close(); s.delF.Close() }()
	s.dirsW, s.delW = bufio.NewWriterSize(s.dirsF, 1<<20), bufio.NewWriterSize(s.delF, 1<<20)

	// Producers: source walk and destination manifest, both in WalkDir order.
	m := NewMatcher(o.Excludes)
	cap := Capture{Owner: o.PreserveOwner, Names: o.PreserveOwner && o.OwnerByName, ACL: o.PreserveACLs}
	srcCh, dstCh := make(chan Entry, 4096), make(chan Entry, 4096)
	srcErr, dstErr := make(chan error, 1), make(chan error, 1)
	push := func(ch chan Entry) func(Entry) error {
		return func(e Entry) error {
			select {
			case ch <- e:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	go func() {
		err := Walk(ctx, o.Src, m, cap, push(srcCh), func(p string, e error) {
			atomic.AddInt64(&s.res.SourceErrors, 1)
			if atomic.AddInt64(&s.logged, 1) <= 50 {
				s.log("warning", "cannot read source %s: %v", p, e)
			}
		})
		close(srcCh)
		srcErr <- err
	}()
	go func() {
		err := t.Manifest(ctx, o.Excludes, cap, push(dstCh))
		close(dstCh)
		dstErr <- err
	}()

	// Workers.
	ops := make(chan op, 1024)
	var wg sync.WaitGroup
	if !o.DryRun {
		for i := 0; i < o.Workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range ops {
					if ctx.Err() != nil {
						continue
					}
					if err := s.run(ctx, p); err != nil && ctx.Err() == nil {
						if errors.Is(err, errTooManyFailures) {
							s.log("error", "%v", err)
							cancel()
						}
					}
				}
			}()
		}
	}
	stopProgress := make(chan struct{})
	go s.progress(stopProgress)

	mergeErr := s.merge(ctx, srcCh, dstCh, ops)
	close(ops)
	wg.Wait()
	close(stopProgress)

	// Producer outcomes decide whether the scan can be trusted.
	if e := <-dstErr; e != nil && mergeErr == nil {
		mergeErr = fmt.Errorf("reading destination: %w", e)
	}
	if e := <-srcErr; e != nil && mergeErr == nil {
		mergeErr = fmt.Errorf("reading source: %w", e)
	}
	if mergeErr != nil {
		return s.res, mergeErr
	}
	if ctx.Err() != nil {
		return s.res, errors.New("cancelled or aborted")
	}
	s.dirsW.Flush()
	s.delW.Flush()

	if !o.DryRun {
		if err := s.applyDirMeta(ctx); err != nil {
			s.log("warning", "directory timestamps/permissions: %v", err)
			atomic.AddInt64(&s.res.Errors, 1)
		}
	}
	s.deletePhase(ctx)
	if !o.DryRun {
		_ = t.Cleanup(ctx)
	}
	return s.res, nil
}

func (s *syncer) progress(stop <-chan struct{}) {
	tk := time.NewTicker(30 * time.Second)
	defer tk.Stop()
	var last int64
	lastT := time.Now()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			b := atomic.LoadInt64(&s.res.BytesCopied)
			rate := float64(b-last) / time.Since(lastT).Seconds() / (1 << 20)
			last, lastT = b, time.Now()
			s.log("info", "progress: scanned %d source / %d destination entries; copied %d files (%.1f GB) at %.0f MB/s; errors %d",
				atomic.LoadInt64(&s.res.SourceEntries), atomic.LoadInt64(&s.res.DestEntries),
				atomic.LoadInt64(&s.res.FilesCopied), float64(b)/(1<<30), rate, atomic.LoadInt64(&s.res.Errors))
		}
	}
}

// merge walks the two ordered streams together and plans the work.
func (s *syncer) merge(ctx context.Context, srcCh, dstCh <-chan Entry, ops chan<- op) error {
	res := s.res
	var sh, dh *Entry
	next := func(ch <-chan Entry) *Entry {
		select {
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			return &e
		case <-ctx.Done():
			return nil
		}
	}
	emit := func(p op) {
		if s.o.DryRun {
			return
		}
		select {
		case ops <- p:
		case <-ctx.Done():
		}
	}
	// skipSubtree drains destination entries beneath dir, returning how many.
	skipSubtree := func(dir string) int64 {
		var n int64
		for dh != nil && strings.HasPrefix(dh.P, dir+"/") {
			n++
			atomic.AddInt64(&res.DestEntries, 1)
			dh = next(dstCh)
		}
		return n
	}
	newEntry := func(e Entry) {
		switch e.T {
		case "d":
			atomic.AddInt64(&res.DirsCreated, 1)
			emit(op{opMkdir, e})
		case "l":
			atomic.AddInt64(&res.Links, 1)
			emit(op{opSymlink, e})
		default:
			atomic.AddInt64(&res.New, 1)
			atomic.AddInt64(&res.BytesPlanned, e.S)
			emit(op{opCopy, e})
		}
	}

	sh, dh = next(srcCh), next(dstCh)
	for (sh != nil || dh != nil) && ctx.Err() == nil {
		switch {
		case dh == nil || (sh != nil && Cmp(sh.P, dh.P) < 0):
			// Only on the source.
			atomic.AddInt64(&res.SourceEntries, 1)
			if sh.T == "f" {
				atomic.AddInt64(&res.SourceBytes, sh.S)
			}
			if sh.T == "d" {
				s.recordDir(*sh)
			}
			newEntry(*sh)
			sh = next(srcCh)

		case sh == nil || Cmp(dh.P, sh.P) < 0:
			// Only on the destination: an extra.
			atomic.AddInt64(&res.DestEntries, 1)
			d := *dh
			dh = next(dstCh)
			n := int64(1)
			if d.T == "d" {
				n += skipSubtree(d.P)
			}
			if s.o.PropagateDeletes {
				atomic.AddInt64(&res.ToDelete, n)
				s.recordDelete(d.P, n)
			} else {
				atomic.AddInt64(&res.KeptExtra, n)
			}

		default:
			// Present on both sides.
			atomic.AddInt64(&res.SourceEntries, 1)
			atomic.AddInt64(&res.DestEntries, 1)
			a, b := *sh, *dh
			sh, dh = next(srcCh), next(dstCh)
			if a.T == "f" {
				atomic.AddInt64(&res.SourceBytes, a.S)
			}
			if a.T == "d" {
				s.recordDir(a)
			}
			if a.T != b.T {
				// Type change: the new entry replaces the old one at write time.
				if b.T == "d" {
					skipSubtree(b.P)
				}
				newEntry(a)
				continue
			}
			switch a.T {
			case "d":
			case "l":
				if a.L != b.L {
					atomic.AddInt64(&res.Links, 1)
					emit(op{opSymlink, a})
				} else if s.metaDiffers(a, b) {
					atomic.AddInt64(&res.MetaFixed, 1)
					emit(op{opMeta, a})
				}
			default:
				sameSize := a.S == b.S
				sameTime := abs64(a.M-b.M) <= mtimeTolerance
				switch {
				case s.o.Compare == "checksum" && sameSize:
					emit(op{opVerify, a})
				case sameSize && sameTime:
					atomic.AddInt64(&res.Unchanged, 1)
					if (a.O&0o7777) != (b.O&0o7777) || s.metaDiffers(a, b) {
						atomic.AddInt64(&res.MetaFixed, 1)
						emit(op{opMeta, a})
					}
				default:
					atomic.AddInt64(&res.Changed, 1)
					atomic.AddInt64(&res.BytesPlanned, a.S)
					emit(op{opCopy, a})
				}
			}
		}
	}
	return ctx.Err()
}

// metaDiffers reports an ownership or ACL/attribute difference between two
// entries that are otherwise identical.
func (s *syncer) metaDiffers(a, b Entry) bool {
	if s.o.PreserveOwner {
		if s.o.OwnerByName && (a.UN != "" || b.UN != "" || a.GN != "" || b.GN != "") {
			if a.UN != b.UN || a.GN != b.GN {
				return true
			}
		} else if a.U != b.U || a.G != b.G {
			return true
		}
	}
	return s.o.PreserveACLs && a.X != b.X
}

// enrich attaches the full metadata to apply (reading attributes now, from the source).
func (s *syncer) enrich(e Entry) Entry {
	if !s.o.PreserveOwner && !s.o.PreserveACLs {
		return e
	}
	m := &Meta{Owner: s.o.PreserveOwner, ByName: s.o.OwnerByName, U: e.U, G: e.G, UN: e.UN, GN: e.GN}
	if s.o.PreserveACLs && e.T != "l" && xattrSupported {
		if xa, err := readXattrs(s.srcPath(e.P)); err == nil {
			m.ACL, m.XA = true, xa
		}
	}
	e.Meta = m
	return e
}

// soft counts metadata that couldn't be applied; the data itself is fine.
func (s *syncer) soft(err error) error {
	var mw *MetaWarning
	if errors.As(err, &mw) {
		if atomic.AddInt64(&s.res.MetaWarnings, 1) == 1 {
			s.log("warning", "some metadata could not be applied on the destination: %s", mw.Msg)
		}
		return nil
	}
	return err
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func (s *syncer) recordDir(e Entry) {
	s.dirMu.Lock()
	b, _ := json.Marshal(e)
	s.dirsW.Write(b)
	s.dirsW.WriteByte('\n')
	s.dirMu.Unlock()
}

func (s *syncer) recordDelete(p string, n int64) {
	s.delMu.Lock()
	b, _ := json.Marshal(struct {
		P string `json:"p"`
		N int64  `json:"n"`
	}{p, n})
	s.delW.Write(b)
	s.delW.WriteByte('\n')
	s.delMu.Unlock()
}

// run executes one operation with retries.
func (s *syncer) run(ctx context.Context, p op) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err = s.exec(ctx, p)
		var se *sourceError
		if err == nil || ctx.Err() != nil || (errors.As(err, &se) && (errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission))) {
			break
		}
	}
	switch {
	case err == nil:
		atomic.StoreInt64(&s.consec, 0)
		return nil
	case isVanished(err):
		atomic.AddInt64(&s.res.Vanished, 1) // deleted at the source mid-run; next run reconciles
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	}
	atomic.AddInt64(&s.res.Errors, 1)
	if atomic.AddInt64(&s.logged, 1) <= 50 {
		s.log("warning", "%s: %v", p.e.P, err)
	}
	if atomic.AddInt64(&s.consec, 1) >= maxConsecFail {
		return errTooManyFailures
	}
	return err
}

func isVanished(err error) bool {
	var se *sourceError
	return errors.As(err, &se) && errors.Is(err, os.ErrNotExist)
}

func (s *syncer) exec(ctx context.Context, p op) error {
	switch p.kind {
	case opMkdir:
		return s.soft(s.t.Mkdir(ctx, s.enrich(p.e)))
	case opSymlink:
		return s.soft(s.t.Symlink(ctx, s.enrich(p.e)))
	case opMeta:
		return s.soft(s.t.SetMeta(ctx, []Entry{s.enrich(p.e)}))
	case opVerify:
		a, err := HashFile(s.srcPath(p.e.P))
		if err != nil {
			return &sourceError{err}
		}
		b, err := s.t.Hash(ctx, p.e.P)
		if err != nil {
			return err
		}
		if a == b {
			atomic.AddInt64(&s.res.Unchanged, 1)
			if p.e.M != 0 {
				if err := s.soft(s.t.SetMeta(ctx, []Entry{s.enrich(p.e)})); err == nil {
					atomic.AddInt64(&s.res.MetaFixed, 1)
				}
			}
			return nil
		}
		atomic.AddInt64(&s.res.Changed, 1)
		atomic.AddInt64(&s.res.BytesPlanned, p.e.S)
		return s.copyFile(ctx, p.e)
	default:
		return s.copyFile(ctx, p.e)
	}
}

func (s *syncer) srcPath(rel string) string { return filepath.Join(s.o.Src, filepath.FromSlash(rel)) }

func (s *syncer) copyFile(ctx context.Context, e Entry) error {
	f, err := os.Open(s.srcPath(e.P))
	if err != nil {
		return &sourceError{err}
	}
	defer f.Close()
	// Use the size/mtime of the file we actually read, so what we record is
	// consistent with the bytes sent; if it changes again we re-sync next run.
	st, err := f.Stat()
	if err != nil {
		return err
	}
	e.S, e.M = st.Size(), st.ModTime().UnixNano()
	if !st.Mode().IsRegular() {
		return nil
	}
	e = s.enrich(e)

	var off int64
	if e.S >= resumeMinSize {
		if off, err = s.t.Offset(ctx, e); err != nil {
			off = 0
		}
	}
	put := func(off int64) error {
		body := io.Reader(io.NewSectionReader(f, off, e.S-off))
		body = &limitedReader{ctx: ctx, r: body, l: s.lim, add: func(n int64) { atomic.AddInt64(&s.res.BytesCopied, n) }}
		return s.t.Put(ctx, e, off, body)
	}
	if off > 0 {
		atomic.AddInt64(&s.res.ResumedFiles, 1)
		atomic.AddInt64(&s.res.ResumedBytes, off)
	}
	err = s.soft(put(off))
	if errors.Is(err, ErrOffset) {
		err = s.soft(put(0)) // partial file was lost or changed; start over
	}
	if err != nil {
		return err
	}
	atomic.AddInt64(&s.res.FilesCopied, 1)
	if st2, err := os.Stat(s.srcPath(e.P)); err == nil && (st2.Size() != e.S || st2.ModTime().UnixNano() != e.M) {
		atomic.AddInt64(&s.res.ChangedDuring, 1)
	}
	return nil
}

// applyDirMeta sets directory permissions/mtimes after all content is written.
func (s *syncer) applyDirMeta(ctx context.Context) error {
	if _, err := s.dirsF.Seek(0, io.SeekStart); err != nil {
		return err
	}
	sc := bufio.NewScanner(s.dirsF)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	batch := make([]Entry, 0, metaBatch)
	var first error
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.soft(s.t.SetMeta(ctx, batch)); err != nil && first == nil {
			first = err
		}
		batch = batch[:0]
	}
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			batch = append(batch, s.enrich(e))
			if len(batch) >= metaBatch {
				flush()
			}
		}
	}
	flush()
	return first
}

// deletePhase removes destination extras, but only when it is safe to.
func (s *syncer) deletePhase(ctx context.Context) {
	r, o := s.res, s.o
	if !o.PropagateDeletes || r.ToDelete == 0 {
		return
	}
	switch {
	case r.SourceErrors > 0:
		r.DeleteBlocked = fmt.Sprintf("%d source path(s) could not be read, so %d destination item(s) were NOT deleted (they may only look missing)", r.SourceErrors, r.ToDelete)
	case r.SourceEntries == 0 && !o.ForceDelete:
		r.DeleteBlocked = fmt.Sprintf("the source is empty but the destination has %d item(s); refusing to delete them (is the source mounted?)", r.ToDelete)
	case !o.ForceDelete && o.MaxDeletePct > 0 && r.ToDelete >= minDeleteGuard && r.ToDelete*100 > r.DestEntries*int64(o.MaxDeletePct):
		r.DeleteBlocked = fmt.Sprintf("would delete %d of %d destination items (%d%%), above the %d%% safety limit; nothing was deleted",
			r.ToDelete, r.DestEntries, r.ToDelete*100/max(r.DestEntries, 1), o.MaxDeletePct)
	}
	if r.DeleteBlocked != "" {
		s.log("error", "%s", r.DeleteBlocked)
		return
	}
	if o.DryRun {
		return
	}
	s.log("info", "removing %d item(s) that no longer exist on the source", r.ToDelete)
	if _, err := s.delF.Seek(0, io.SeekStart); err != nil {
		return
	}
	sc := bufio.NewScanner(s.delF)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	var paths []string
	var counts int64
	flush := func() {
		if len(paths) == 0 || ctx.Err() != nil {
			return
		}
		var err error
		for a := 0; a < 3; a++ {
			if err = s.t.Delete(ctx, paths); err == nil {
				break
			}
			time.Sleep(time.Second << a)
		}
		if err != nil {
			atomic.AddInt64(&r.Errors, 1)
			s.log("warning", "delete failed: %v", err)
		} else {
			atomic.AddInt64(&r.Deleted, counts)
		}
		paths, counts = paths[:0], 0
	}
	for sc.Scan() {
		var d struct {
			P string `json:"p"`
			N int64  `json:"n"`
		}
		if json.Unmarshal(sc.Bytes(), &d) == nil {
			paths = append(paths, d.P)
			counts += d.N
			if len(paths) >= deleteBatch {
				flush()
			}
		}
	}
	flush()
}
