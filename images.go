package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// The images heedle gives its worker, so browse and agent steps run on a
// fresh install with nothing to build (roadmap 9.1). reed and pirn
// publish them (their image.yml workflows, tested before they're pushed);
// heedle pins each by its manifest list's digest, so a new image is a new
// commit here, never a moving tag.
//
//go:embed images.json
var imagesJSON []byte

// images are the pinned refs in images.json.
type images struct {
	Browser string `json:"browser"` // reed's browser image, for browse steps
	Agent   string `json:"agent"`   // pirn's agent image, for agent and skill steps
}

// pinnedRef is a ref pinned to content: name@sha256:<64 hex>.
var pinnedRef = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*@sha256:[0-9a-f]{64}$`)

// readImages reads images.json strictly: one object, both images, each
// pinned to content, nothing else.
func readImages(b []byte) (images, error) {
	var im images
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&im); err != nil {
		return images{}, fmt.Errorf("images.json: %w", err)
	}
	if d.More() {
		return images{}, fmt.Errorf("images.json: more than one object")
	}
	for name, ref := range map[string]string{"browser": im.Browser, "agent": im.Agent} {
		if !pinnedRef.MatchString(ref) {
			return images{}, fmt.Errorf("images.json: %s image %q isn't pinned to content (name@sha256:<64 hex>)", name, ref)
		}
	}
	return im, nil
}

// pinned are heedle's images. images.json is checked by TestInv_ImagesArePinned.
var pinned = func() images {
	im, err := readImages(imagesJSON)
	if err != nil {
		panic(err)
	}
	return im
}()

// hasFlag says whether args (drawdown's, after --) set the flag name.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if strings.HasPrefix(a, "-") && strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-") == name {
			return true
		}
	}
	return false
}

// withImages makes sure heedle's images are here, pulling each once, and
// returns the flags that give them to the worker. An image given after --
// is the person's: it isn't pulled or replaced. An image that can't be
// pulled is said, in a line, and left out, so heedle still starts and the
// front door says which steps can't run.
func withImages(ctx context.Context, sys system, podman string, uiArgs []string, im images, out io.Writer) []string {
	var flags []string
	for _, i := range []struct{ flag, ref, what string }{
		{"browser-image", im.Browser, "browser image (for browse steps)"},
		{"agent-image", im.Agent, "agent image (for agent and skill steps)"},
	} {
		if hasFlag(uiArgs, i.flag) {
			continue
		}
		if _, err := sys.output(ctx, podman, "image", "exists", i.ref); err != nil {
			fmt.Fprintf(out, "Downloading the %s, once…\n", i.what)
			if _, err := sys.output(ctx, podman, "pull", "--quiet", i.ref); err != nil {
				fmt.Fprintf(out, "Couldn't download the %s (%v), so those steps won't run. Run heedle up again when you're online, or give your own with heedle up -- -%s ID.\n", i.what, err, i.flag)
				continue
			}
		}
		flags = append(flags, "-"+i.flag, i.ref)
	}
	return flags
}
