// catalog-images prints every image the catalog deploys, one per line,
// sorted: each image template's, and each compose template's services'.
// scripts/check-catalog-images.sh checks that each can still be pulled.
package main

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
)

func main() {
	images, err := catalogImages(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog-images:", err)
		os.Exit(1)
	}
	for _, img := range images {
		fmt.Println(img)
	}
}

func catalogImages(ctx context.Context) ([]string, error) {
	images := make([]string, 0, len(catalog.All()))
	for _, t := range catalog.All() {
		if t.Kind != catalog.KindCompose {
			images = append(images, t.DockerImage)
			continue
		}
		// Every variable set, as a deploy sets them, so a required one
		// (${TOFT_URL:?…}) doesn't stop the file loading.
		env := map[string]string{"TOFT_URL": "https://app.example.com"}
		for _, s := range t.EnvVarSpecs {
			env[s.Key] = "x"
		}
		p, vs, err := composespec.Load(ctx, deploypolicy.InlineComposeSource("00000000-0000-0000-0000-000000000000", t.Slug, t.ComposeContent, env))
		if err != nil {
			return nil, fmt.Errorf("loading %s's compose file: %w", t.Slug, err)
		}
		if p == nil {
			return nil, fmt.Errorf("loading %s's compose file: %+v", t.Slug, vs)
		}
		for _, s := range p.Services {
			images = append(images, s.Image)
		}
	}
	slices.Sort(images)
	return slices.Compact(images), nil
}
