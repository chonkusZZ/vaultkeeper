package mirror

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Capture selects which extra metadata is read (source and destination) and compared.
type Capture struct {
	Owner bool // uid/gid
	Names bool // also record user/group names, and compare by name
	ACL   bool // ACLs and extended attributes
}

// Meta is the metadata to apply to a destination item.
type Meta struct {
	Owner  bool              `json:"ow,omitempty"`
	ByName bool              `json:"bn,omitempty"`
	U      int               `json:"u,omitempty"`
	G      int               `json:"g,omitempty"`
	UN     string            `json:"un,omitempty"`
	GN     string            `json:"gn,omitempty"`
	ACL    bool              `json:"acl,omitempty"`
	XA     map[string][]byte `json:"xa,omitempty"`
}

// MetaWarning reports metadata that could not be applied although the data
// itself was written (typically: the destination agent isn't root).
type MetaWarning struct{ Msg string }

func (w *MetaWarning) Error() string { return w.Msg }

// Features describes what this platform can preserve.
type Features struct {
	Owner bool
	XAttr bool
	Note  string
}

func Capabilities() Features { return platformFeatures() }

// modeBits keeps permission bits plus setuid/setgid/sticky.
func modeBits(m os.FileMode) uint32 {
	o := uint32(m.Perm())
	if m&os.ModeSetuid != 0 {
		o |= 0o4000
	}
	if m&os.ModeSetgid != 0 {
		o |= 0o2000
	}
	if m&os.ModeSticky != 0 {
		o |= 0o1000
	}
	return o
}

func osMode(o uint32) os.FileMode {
	m := os.FileMode(o & 0o777)
	if o&0o4000 != 0 {
		m |= os.ModeSetuid
	}
	if o&0o2000 != 0 {
		m |= os.ModeSetgid
	}
	if o&0o1000 != 0 {
		m |= os.ModeSticky
	}
	return m
}

var nameCache sync.Map

func cached(key string, fn func() string) string {
	if v, ok := nameCache.Load(key); ok {
		return v.(string)
	}
	v := fn()
	nameCache.Store(key, v)
	return v
}

func userName(uid int) string {
	return cached("u"+strconv.Itoa(uid), func() string {
		if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
			return u.Username
		}
		return ""
	})
}

func groupName(gid int) string {
	return cached("g"+strconv.Itoa(gid), func() string {
		if g, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil {
			return g.Name
		}
		return ""
	})
}

func lookupUID(name string, fallback int) int {
	if name == "" {
		return fallback
	}
	if u, err := user.Lookup(name); err == nil {
		if id, err := strconv.Atoi(u.Uid); err == nil {
			return id
		}
	}
	return fallback
}

func lookupGID(name string, fallback int) int {
	if name == "" {
		return fallback
	}
	if g, err := user.LookupGroup(name); err == nil {
		if id, err := strconv.Atoi(g.Gid); err == nil {
			return id
		}
	}
	return fallback
}

// readXattrs returns the managed extended attributes of path (nil if none or unsupported).
func readXattrs(path string) (map[string][]byte, error) {
	names, err := listXattr(path)
	if err != nil {
		return nil, err
	}
	var out map[string][]byte
	for _, n := range names {
		if !managedXattr(n) {
			continue
		}
		v, err := getXattr(path, n)
		if err != nil {
			continue // vanished or unreadable: skip
		}
		if out == nil {
			out = map[string][]byte{}
		}
		out[n] = v
	}
	return out, nil
}

// xattrDigest is a short stable fingerprint of an attribute set ("" when empty).
func xattrDigest(m map[string][]byte) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha1.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%d\x00", k, len(m[k]))
		h.Write(m[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// applyXattrs makes path's managed attributes exactly equal to want.
func applyXattrs(path string, want map[string][]byte) error {
	cur, err := readXattrs(path)
	if err != nil {
		return err
	}
	var first error
	for n := range cur {
		if _, keep := want[n]; !keep {
			if err := removeXattr(path, n); err != nil && first == nil {
				first = fmt.Errorf("removing attribute %s: %w", n, err)
			}
		}
	}
	for n, v := range want {
		if old, ok := cur[n]; ok && string(old) == string(v) {
			continue
		}
		if err := setXattr(path, n, v); err != nil && first == nil {
			first = fmt.Errorf("setting attribute %s: %w", n, err)
		}
	}
	return first
}

// ownerCaptured fills the ownership fields of e from fi.
func (c Capture) fill(e *Entry, path string, fi os.FileInfo) {
	if c.Owner {
		if u, g, ok := ownerOf(fi); ok {
			e.U, e.G = u, g
			if c.Names {
				e.UN, e.GN = userName(u), groupName(g)
			}
		}
	}
	if c.ACL && e.T != "l" && xattrSupported {
		if xa, err := readXattrs(path); err == nil {
			e.X = xattrDigest(xa)
		}
	}
}

func warnJoin(ws []string) error {
	if len(ws) == 0 {
		return nil
	}
	return &MetaWarning{Msg: strings.Join(ws, "; ")}
}
