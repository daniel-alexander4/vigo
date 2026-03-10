package traits

import (
	"context"
	"os"
	"os/user"
	"strconv"
)

// IdentityCollector gathers identity traits: uid, gid, user, group,
// and whether the process is running as root.
type IdentityCollector struct{}

func (c *IdentityCollector) Name() string { return "identity" }

func (c *IdentityCollector) Collect(_ context.Context) (map[string]any, error) {
	u, err := user.Current()
	if err != nil {
		return nil, err
	}

	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	groupName := u.Gid
	if g, err := user.LookupGroupId(u.Gid); err == nil {
		groupName = g.Name
	}

	return map[string]any{
		"uid":        uid,
		"gid":        gid,
		"user":       u.Username,
		"group":      groupName,
		"privileged": os.Geteuid() == 0,
	}, nil
}
