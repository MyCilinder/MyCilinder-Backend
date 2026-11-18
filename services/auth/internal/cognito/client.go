// Package cognito is a thin adapter over the Amazon Cognito user pool API
// (Interface Design §3.1). It computes SECRET_HASH for the confidential app client and
// maps Cognito exceptions to the domain errors in errors.go, so the rest of the service
// never imports the AWS SDK types.
package cognito

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

// API is the subset of the SDK client the adapter uses. *cip.Client satisfies it; tests
// pass a stub.
type API interface {
	SignUp(ctx context.Context, in *cip.SignUpInput, opts ...func(*cip.Options)) (*cip.SignUpOutput, error)
	ConfirmSignUp(ctx context.Context, in *cip.ConfirmSignUpInput, opts ...func(*cip.Options)) (*cip.ConfirmSignUpOutput, error)
	ResendConfirmationCode(ctx context.Context, in *cip.ResendConfirmationCodeInput, opts ...func(*cip.Options)) (*cip.ResendConfirmationCodeOutput, error)
	InitiateAuth(ctx context.Context, in *cip.InitiateAuthInput, opts ...func(*cip.Options)) (*cip.InitiateAuthOutput, error)
	GetTokensFromRefreshToken(ctx context.Context, in *cip.GetTokensFromRefreshTokenInput, opts ...func(*cip.Options)) (*cip.GetTokensFromRefreshTokenOutput, error)
	RevokeToken(ctx context.Context, in *cip.RevokeTokenInput, opts ...func(*cip.Options)) (*cip.RevokeTokenOutput, error)
	GetUser(ctx context.Context, in *cip.GetUserInput, opts ...func(*cip.Options)) (*cip.GetUserOutput, error)
}

// Client calls Cognito for one confidential app client.
type Client struct {
	api          API
	clientID     string
	clientSecret string
}

// New returns a Client.
func New(api API, clientID, clientSecret string) *Client {
	return &Client{api: api, clientID: clientID, clientSecret: clientSecret}
}

// Tokens is the result of a login or refresh. RefreshToken is empty after a refresh,
// because refresh token rotation is disabled on the app client.
type Tokens struct {
	AccessToken  string
	IDToken      string
	RefreshToken string
	ExpiresIn    int32
}

// UserInfo holds the attributes GET /me needs.
type UserInfo struct {
	Sub         string
	Email       string
	DisplayName string
}

// SecretHash computes SECRET_HASH = Base64(HMAC-SHA256(clientSecret, username + clientID)).
func SecretHash(username, clientID, clientSecret string) string {
	mac := hmac.New(sha256.New, []byte(clientSecret))
	mac.Write([]byte(username + clientID))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (c *Client) hash(username string) *string {
	return aws.String(SecretHash(username, c.clientID, c.clientSecret))
}

// SignUp registers email with password and returns the new user's sub. The display name
// is stored in the standard `name` attribute so it is available on first login
// (FR-AUTH-01, FR-AUTH-05).
func (c *Client) SignUp(ctx context.Context, email, password, displayName string) (string, error) {
	out, err := c.api.SignUp(ctx, &cip.SignUpInput{
		ClientId:   aws.String(c.clientID),
		SecretHash: c.hash(email),
		Username:   aws.String(email),
		Password:   aws.String(password),
		UserAttributes: []types.AttributeType{
			{Name: aws.String("email"), Value: aws.String(email)},
			{Name: aws.String("name"), Value: aws.String(displayName)},
		},
	})
	if err != nil {
		return "", mapError(err)
	}
	return aws.ToString(out.UserSub), nil
}

// ConfirmSignUp confirms the account with the emailed code (FR-AUTH-02).
func (c *Client) ConfirmSignUp(ctx context.Context, email, code string) error {
	_, err := c.api.ConfirmSignUp(ctx, &cip.ConfirmSignUpInput{
		ClientId:         aws.String(c.clientID),
		SecretHash:       c.hash(email),
		Username:         aws.String(email),
		ConfirmationCode: aws.String(code),
	})
	return mapError(err)
}

// ResendCode sends a new verification code (UC-01 5a).
func (c *Client) ResendCode(ctx context.Context, email string) error {
	_, err := c.api.ResendConfirmationCode(ctx, &cip.ResendConfirmationCodeInput{
		ClientId:   aws.String(c.clientID),
		SecretHash: c.hash(email),
		Username:   aws.String(email),
	})
	return mapError(err)
}

// Login runs USER_PASSWORD_AUTH (FR-AUTH-03). The pool has no MFA, so a challenge in the
// response is unexpected and reported as ErrUnavailable.
func (c *Client) Login(ctx context.Context, email, password string) (Tokens, error) {
	out, err := c.api.InitiateAuth(ctx, &cip.InitiateAuthInput{
		ClientId: aws.String(c.clientID),
		AuthFlow: types.AuthFlowTypeUserPasswordAuth,
		AuthParameters: map[string]string{
			"USERNAME":    email,
			"PASSWORD":    password,
			"SECRET_HASH": SecretHash(email, c.clientID, c.clientSecret),
		},
	})
	if err != nil {
		return Tokens{}, mapError(err)
	}
	if out.AuthenticationResult == nil {
		return Tokens{}, errors.Join(ErrUnavailable, errors.New("cognito: unexpected challenge "+string(out.ChallengeName)))
	}
	return tokensFrom(out.AuthenticationResult), nil
}

// Refresh exchanges a refresh token for new access and ID tokens (FR-AUTH-04).
//
// It uses GetTokensFromRefreshToken, which accepts the client secret directly. The
// alternative, InitiateAuth with REFRESH_TOKEN_AUTH, needs a SECRET_HASH built from the
// user's sub, which the facade cannot read from the opaque refresh token, and the
// RefreshRequest contract carries only the refresh token.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	out, err := c.api.GetTokensFromRefreshToken(ctx, &cip.GetTokensFromRefreshTokenInput{
		ClientId:     aws.String(c.clientID),
		ClientSecret: aws.String(c.clientSecret),
		RefreshToken: aws.String(refreshToken),
	})
	if err != nil {
		return Tokens{}, mapError(err)
	}
	if out.AuthenticationResult == nil {
		return Tokens{}, errors.Join(ErrUnavailable, errors.New("cognito: empty refresh result"))
	}
	t := tokensFrom(out.AuthenticationResult)
	t.RefreshToken = "" // rotation is disabled; never hand back a token the client already has
	return t, nil
}

// Revoke revokes the refresh token and the access and ID tokens issued from it
// (FR-AUTH-06).
func (c *Client) Revoke(ctx context.Context, refreshToken string) error {
	_, err := c.api.RevokeToken(ctx, &cip.RevokeTokenInput{
		ClientId:     aws.String(c.clientID),
		ClientSecret: aws.String(c.clientSecret),
		Token:        aws.String(refreshToken),
	})
	return mapError(err)
}

// GetUser reads the caller's attributes with their access token (GET /me).
func (c *Client) GetUser(ctx context.Context, accessToken string) (UserInfo, error) {
	out, err := c.api.GetUser(ctx, &cip.GetUserInput{AccessToken: aws.String(accessToken)})
	if err != nil {
		return UserInfo{}, mapError(err)
	}
	var info UserInfo
	for _, a := range out.UserAttributes {
		switch aws.ToString(a.Name) {
		case "sub":
			info.Sub = aws.ToString(a.Value)
		case "email":
			info.Email = aws.ToString(a.Value)
		case "name":
			info.DisplayName = aws.ToString(a.Value)
		}
	}
	return info, nil
}

func tokensFrom(r *types.AuthenticationResultType) Tokens {
	return Tokens{
		AccessToken:  aws.ToString(r.AccessToken),
		IDToken:      aws.ToString(r.IdToken),
		RefreshToken: aws.ToString(r.RefreshToken),
		ExpiresIn:    r.ExpiresIn,
	}
}
