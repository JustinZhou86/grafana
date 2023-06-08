package angulardetector

import (
	"bytes"
	"regexp"
)

var (
	_ detector = &containsBytesDetector{}
	_ detector = &regexDetector{}
)

// detector implements a check to see if a plugin uses Angular.
type detector interface {
	// Detect takes the content of a moduleJs file and returns true if the plugin is using Angular.
	Detect(moduleJs []byte) bool
}

// containsBytesDetector is a detector that returns true if module.js contains the "pattern" string.
type containsBytesDetector struct {
	pattern []byte
}

// Detect returns true if moduleJs contains the byte slice d.pattern.
func (d *containsBytesDetector) Detect(moduleJs []byte) bool {
	return bytes.Contains(moduleJs, d.pattern)
}

// regexDetector is a detector that returns true if the module.js content matches a regular expression.
type regexDetector struct {
	regex *regexp.Regexp
}

// Detect returns true if moduleJs matches the regular expression d.regex.
func (d *regexDetector) Detect(moduleJs []byte) bool {
	return d.regex.Match(moduleJs)
}
