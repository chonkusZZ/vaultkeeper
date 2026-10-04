//go:build !linux && !darwin

package mirror

import "errors"

const xattrSupported = false

func platformFeatures() Features {
	return Features{Owner: false, XAttr: false, Note: "ownership, ACLs and extended attributes are not supported on this platform"}
}

func listXattr(path string) ([]string, error)    { return nil, nil }
func getXattr(path, name string) ([]byte, error) { return nil, errors.New("unsupported") }
func setXattr(path, name string, v []byte) error {
	return errors.New("extended attributes are not supported on this platform")
}
func removeXattr(path, name string) error { return nil }
func managedXattr(name string) bool       { return false }
