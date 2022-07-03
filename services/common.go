package services

import "regexp"

var sha1R = regexp.MustCompile("^[0-9a-f]{5,40}$")
