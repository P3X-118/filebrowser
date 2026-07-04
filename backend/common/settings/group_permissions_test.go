package settings

import (
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/database/users"
)

func TestPermissionsFromGroups(t *testing.T) {
	grants := map[string]users.Permissions{
		"editors": {Create: true, Modify: true, Delete: true, Share: true},
		"admins":  {Admin: true},
	}
	base := users.Permissions{Download: true, Realtime: true}

	cases := []struct {
		name   string
		groups []string
		want   users.Permissions
	}{
		{
			name:   "no groups keeps the floor",
			groups: nil,
			want:   base,
		},
		{
			name:   "unmapped groups keep the floor",
			groups: []string{"trainees"},
			want:   base,
		},
		{
			name:   "single group unions its grants over the floor",
			groups: []string{"editors"},
			want:   users.Permissions{Download: true, Realtime: true, Create: true, Modify: true, Delete: true, Share: true},
		},
		{
			name:   "multiple groups union all grants",
			groups: []string{"editors", "admins", "trainees"},
			want:   users.Permissions{Download: true, Realtime: true, Create: true, Modify: true, Delete: true, Share: true, Admin: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PermissionsFromGroups(base, grants, tc.groups)
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
