package oci

import (
	"reflect"
	"testing"
)

func TestBuildahBudArgs_localLayerCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		imageRef        string
		dockerfilePath  string
		localLayerCache bool
		want            []string
	}{
		{
			name:            "with layers",
			imageRef:        "kindling/foo:abcd",
			dockerfilePath:  "Dockerfile",
			localLayerCache: true,
			want:            []string{"bud", "--layers", "-t", "kindling/foo:abcd", "-f", "Dockerfile", "."},
		},
		{
			name:            "without layers",
			imageRef:        "kindling/bar:ef12",
			dockerfilePath:  "infra/Containerfile",
			localLayerCache: false,
			want:            []string{"bud", "-t", "kindling/bar:ef12", "-f", "infra/Containerfile", "."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := BuildahBudArgs(tt.imageRef, tt.dockerfilePath, tt.localLayerCache)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("BuildahBudArgs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
