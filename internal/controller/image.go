package controller

import (
	"fmt"
	"strings"
)

// imageRef constructs the full OCI reference for the shekel-controller image.
// owner is normalised to lowercase as required by ghcr.io.
func imageRef(owner, repo, tag string) string {
	owner = strings.ToLower(owner)
	if tag == "" {
		tag = "latest"
	}
	return fmt.Sprintf("ghcr.io/%s/%s/%s", owner, repo, tag)
}
