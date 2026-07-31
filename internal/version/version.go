// Package version carries the version this build was made from.
//
// It is a package of its own rather than a variable in main because main is not
// the only thing that needs it: the web interface shows it, and anything that
// one day compares this build against a published release has to read it too. A
// linker flag can name exactly one symbol, so there had better be exactly one.
package version

// Version is stamped in at build time by host/install.sh, which asks git rather
// than a file in the tree. A version written down somewhere can disagree with
// the tag it was cut from; one derived from the tag cannot.
//
// The default is what a plain "go build" leaves behind, and it says "dev" on
// purpose: a binary nobody stamped should not claim to be a release.
var Version = "dev"
