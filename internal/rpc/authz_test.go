package rpc

import "testing"

func TestOrgRoleCanManage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role string
		want bool
	}{
		{role: "owner", want: true},
		{role: "admin", want: true},
		{role: "member", want: false},
		{role: "", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.role, func(t *testing.T) {
			t.Parallel()
			if got := orgRoleCanManage(tt.role); got != tt.want {
				t.Fatalf("orgRoleCanManage(%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}
