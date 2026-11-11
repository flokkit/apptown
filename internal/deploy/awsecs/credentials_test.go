package awsecs

import (
	"errors"
	"github.com/aws/smithy-go"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// An ambient credential must never become the authority for an AWS target.
// Inspect every production file in the module that uses the AWS SDK, not only
// this package: any caller could otherwise build a client from the default chain.
func TestAWSClientsRequireExplicitCredentials(t *testing.T) {
	sdkUsers := 0
	err := filepath.WalkDir("../../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", "private", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		imports, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		usesSDK := false
		for _, im := range imports.Imports {
			if strings.Contains(im.Path.Value, "github.com/aws/") {
				usesSDK = true
			}
		}
		if !usesSDK {
			return nil
		}
		sdkUsers++
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			for _, forbidden := range []string{"/config", "/feature/ec2/imds", "/service/sso", "/service/ssooidc", "/service/signin"} {
				if strings.Contains(p, "aws-sdk-go-v2") && strings.Contains(p, forbidden) {
					t.Errorf("%s imports ambient credential source %s", path, p)
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				for _, name := range []string{"LoadDefaultConfig", "NewEnvCredentials", "NewSharedCredentials", "NewInstanceRoleProvider", "NewEC2RoleProvider"} {
					if id.Name == name {
						t.Errorf("%s uses %s", path, name)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sdkUsers < 2 {
		t.Fatalf("found %d AWS SDK users; the walk no longer reaches the module", sdkUsers)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	if _, err := NewAPI("us-east-1", "", ""); err == nil {
		t.Fatal("accepted ambient credentials")
	}
	a, err := NewAPI("us-east-1", "explicit-key", "explicit-secret")
	if err != nil {
		t.Fatal(err)
	}
	if a.STS == nil || a.ECS == nil {
		t.Fatal("clients absent")
	}
}

func TestSDKDiagnosticsDoNotExposeTheSuppliedKeys(t *testing.T) {
	for _, err := range []error{errors.New("request explicit-key signed with explicit-secret failed"), &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "explicit-key explicit-secret"}} {
		clean := sanitizedError(err, "explicit-key", "explicit-secret")
		if strings.Contains(clean.Error(), "explicit-key") || strings.Contains(clean.Error(), "explicit-secret") {
			t.Fatal(clean)
		}
		var raw interface{ Unwrap() error }
		if errors.As(clean, &raw) {
			t.Fatal("raw credentials can be unwrapped")
		}
	}
	clean := sanitizedError(&smithy.GenericAPIError{Code: "DryRunOperation", Message: "ok"}, "key", "secret")
	var api smithy.APIError
	if !errors.As(clean, &api) || api.ErrorCode() != "DryRunOperation" {
		t.Fatal("lost API error classification")
	}
}
