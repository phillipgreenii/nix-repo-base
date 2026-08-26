package fixture

// Config holds fixture configuration data. Its filename ("config.go") sorts
// before "widget.go", so this cross-check fixture also exercises the
// sorted-filename-order requirement both pgm_pkg_hash (bash) and
// pkghash.Compute (Go) share.
type Config struct {
	Enabled bool
}
