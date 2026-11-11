package awsecs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

const Platform = "linux/amd64"

// Config names the administrator's estate and secrets, never their values.
type Config struct {
	KeyIDFrom, SecretKeyFrom                                  string
	Region, Account, Cluster, Listener, ExecutionRole, Domain string
	TaskSubnets, DatabaseSubnets                              []string
}

func (c Config) Validate() error {
	if !regexp.MustCompile(`^[0-9]{12}$`).MatchString(c.Account) {
		return fmt.Errorf("name the expected 12-digit AWS account")
	}
	if c.Region == "" || c.Cluster == "" || c.Listener == "" || c.ExecutionRole == "" || c.Domain == "" || len(c.TaskSubnets) == 0 {
		return fmt.Errorf("AWS needs region, cluster, listener, executionRole, domain and taskSubnets")
	}
	if strings.Contains(c.Cluster, "/") || strings.Contains(c.Cluster, ":") {
		return fmt.Errorf("cluster must be its name, not its ARN")
	}
	return nil
}

func installationPart(id string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(id)))[:8] }

// Options supplies interface fakes and an in-memory registry in tests.
type Options struct {
	API       func(Config, string, string) (*API, error)
	CopyImage func(context.Context, string, string) (string, error)
}
