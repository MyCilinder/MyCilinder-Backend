// Command cognito-smoke checks the Cognito adapter against a real user pool. It is a
// development tool, not part of the service image.
//
// It reads COGNITO_REGION, COGNITO_CLIENT_ID, and COGNITO_CLIENT_SECRET from the
// environment (the same variables as the service), then:
//
//	cognito-smoke signup  <email> <displayName>   # password read from SMOKE_PASSWORD
//	cognito-smoke confirm <email> <code>
//	cognito-smoke resend  <email>
//	cognito-smoke login   <email>                 # password read from SMOKE_PASSWORD
//
// login prints the sub and token lifetimes, never the tokens themselves.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	libconfig "github.com/MyCilinder/MyCilinder-Backend/libs/go/config"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/cognito"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: cognito-smoke signup|confirm|resend|login <email> [arg]")
	}
	env := libconfig.FromEnv()
	region := env.Required("COGNITO_REGION")
	clientID := env.Required("COGNITO_CLIENT_ID")
	secret := env.Required("COGNITO_CLIENT_SECRET")
	if err := env.Err(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sdk, err := cognito.NewSDKClient(ctx, region)
	if err != nil {
		return err
	}
	c := cognito.New(sdk, clientID, secret)

	cmd, email := args[0], args[1]
	switch cmd {
	case "signup":
		if len(args) < 3 {
			return fmt.Errorf("signup needs <email> <displayName>")
		}
		if err := c.SignUp(ctx, email, password(), args[2]); err != nil {
			return err
		}
		fmt.Println("sign-up accepted; check the inbox for the code")
	case "confirm":
		if len(args) < 3 {
			return fmt.Errorf("confirm needs <email> <code>")
		}
		if err := c.ConfirmSignUp(ctx, email, args[2]); err != nil {
			return err
		}
		fmt.Println("confirmed")
	case "resend":
		if err := c.ResendCode(ctx, email); err != nil {
			return err
		}
		fmt.Println("code resent")
	case "login":
		tok, err := c.Login(ctx, email, password())
		if err != nil {
			return err
		}
		fmt.Printf("login ok: sub=%s expiresIn=%ds refreshToken=%t\n",
			claim(tok.IDToken, "sub"), tok.ExpiresIn, tok.RefreshToken != "")
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

func password() string {
	p := os.Getenv("SMOKE_PASSWORD")
	if p == "" {
		fmt.Fprintln(os.Stderr, "SMOKE_PASSWORD is not set")
		os.Exit(2)
	}
	return p
}

// claim decodes one claim from a JWT payload without verifying it; display only.
func claim(jwt, name string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return "?"
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "?"
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return "?"
	}
	v, _ := m[name].(string)
	return v
}
