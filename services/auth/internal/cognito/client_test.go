package cognito

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/aws/smithy-go"
)

const (
	testClientID = "1example23456789"
	testSecret   = "secret-0123456789abcdefghijklmnop"
)

// Reference value computed independently:
//
//	printf '%s' 'diego@example.com1example23456789' |
//	  openssl dgst -sha256 -hmac 'secret-0123456789abcdefghijklmnop' -binary | base64
func TestSecretHashKnownVector(t *testing.T) {
	const want = "jrCJsXdhT5u/Kf2gEutCoh6ovformrK5ngDukSwMj0w="
	if got := SecretHash("diego@example.com", testClientID, testSecret); got != want {
		t.Fatalf("SecretHash = %q, want %q", got, want)
	}
}

// stubAPI records the last input and returns err (or a canned success).
type stubAPI struct {
	err    error
	last   any
	result *types.AuthenticationResultType
	attrs  []types.AttributeType
}

func (s *stubAPI) SignUp(_ context.Context, in *cip.SignUpInput, _ ...func(*cip.Options)) (*cip.SignUpOutput, error) {
	s.last = in
	if s.err != nil {
		return nil, s.err
	}
	return &cip.SignUpOutput{UserSub: aws.String("new-sub")}, nil
}
func (s *stubAPI) ConfirmSignUp(_ context.Context, in *cip.ConfirmSignUpInput, _ ...func(*cip.Options)) (*cip.ConfirmSignUpOutput, error) {
	s.last = in
	return &cip.ConfirmSignUpOutput{}, s.err
}
func (s *stubAPI) ResendConfirmationCode(_ context.Context, in *cip.ResendConfirmationCodeInput, _ ...func(*cip.Options)) (*cip.ResendConfirmationCodeOutput, error) {
	s.last = in
	return &cip.ResendConfirmationCodeOutput{}, s.err
}
func (s *stubAPI) InitiateAuth(_ context.Context, in *cip.InitiateAuthInput, _ ...func(*cip.Options)) (*cip.InitiateAuthOutput, error) {
	s.last = in
	if s.err != nil {
		return nil, s.err
	}
	return &cip.InitiateAuthOutput{AuthenticationResult: s.result}, nil
}
func (s *stubAPI) GetTokensFromRefreshToken(_ context.Context, in *cip.GetTokensFromRefreshTokenInput, _ ...func(*cip.Options)) (*cip.GetTokensFromRefreshTokenOutput, error) {
	s.last = in
	if s.err != nil {
		return nil, s.err
	}
	return &cip.GetTokensFromRefreshTokenOutput{AuthenticationResult: s.result}, nil
}
func (s *stubAPI) RevokeToken(_ context.Context, in *cip.RevokeTokenInput, _ ...func(*cip.Options)) (*cip.RevokeTokenOutput, error) {
	s.last = in
	return &cip.RevokeTokenOutput{}, s.err
}
func (s *stubAPI) GetUser(_ context.Context, in *cip.GetUserInput, _ ...func(*cip.Options)) (*cip.GetUserOutput, error) {
	s.last = in
	if s.err != nil {
		return nil, s.err
	}
	return &cip.GetUserOutput{UserAttributes: s.attrs}, nil
}

func apiErr(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: code + " message"}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		code string
		want error
	}{
		{"UsernameExistsException", ErrUserExists},
		{"InvalidPasswordException", ErrInvalidPassword},
		{"CodeMismatchException", ErrInvalidCode},
		{"ExpiredCodeException", ErrExpiredCode},
		{"UserNotConfirmedException", ErrNotConfirmed},
		{"NotAuthorizedException", ErrNotAuthorized},
		{"RefreshTokenReuseException", ErrNotAuthorized},
		{"UserNotFoundException", ErrUserNotFound},
		{"InvalidParameterException", ErrInvalidParameter},
		{"TooManyRequestsException", ErrTooManyRequests},
		{"LimitExceededException", ErrTooManyRequests},
		{"InternalErrorException", ErrUnavailable},
		{"SomethingNewException", ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			c := New(&stubAPI{err: apiErr(tt.code)}, testClientID, testSecret)
			_, err := c.SignUp(context.Background(), "a@b.co", "pw", "A")
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}

	t.Run("network error is unavailable", func(t *testing.T) {
		c := New(&stubAPI{err: context.DeadlineExceeded}, testClientID, testSecret)
		err := c.ResendCode(context.Background(), "a@b.co")
		if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("invalid password keeps the policy message", func(t *testing.T) {
		c := New(&stubAPI{err: apiErr("InvalidPasswordException")}, testClientID, testSecret)
		_, err := c.SignUp(context.Background(), "a@b.co", "pw", "A")
		var pe *InvalidPasswordError
		if !errors.As(err, &pe) || pe.Message != "InvalidPasswordException message" {
			t.Fatalf("got %v", err)
		}
	})
}

func TestSignUpRequest(t *testing.T) {
	stub := &stubAPI{}
	c := New(stub, testClientID, testSecret)
	sub, err := c.SignUp(context.Background(), "diego@example.com", "Secret#123", "Diego")
	if err != nil || sub != "new-sub" {
		t.Fatalf("sub=%q err=%v", sub, err)
	}
	in := stub.last.(*cip.SignUpInput)
	if aws.ToString(in.SecretHash) != SecretHash("diego@example.com", testClientID, testSecret) {
		t.Error("wrong secret hash")
	}
	attrs := map[string]string{}
	for _, a := range in.UserAttributes {
		attrs[aws.ToString(a.Name)] = aws.ToString(a.Value)
	}
	if attrs["email"] != "diego@example.com" || attrs["name"] != "Diego" {
		t.Errorf("attributes = %v", attrs)
	}
}

func TestLogin(t *testing.T) {
	stub := &stubAPI{result: &types.AuthenticationResultType{
		AccessToken: aws.String("at"), IdToken: aws.String("it"), RefreshToken: aws.String("rt"), ExpiresIn: 3600,
	}}
	c := New(stub, testClientID, testSecret)
	tok, err := c.Login(context.Background(), "diego@example.com", "Secret#123")
	if err != nil {
		t.Fatal(err)
	}
	if tok != (Tokens{AccessToken: "at", IDToken: "it", RefreshToken: "rt", ExpiresIn: 3600}) {
		t.Errorf("tokens = %+v", tok)
	}
	in := stub.last.(*cip.InitiateAuthInput)
	if in.AuthFlow != types.AuthFlowTypeUserPasswordAuth || in.AuthParameters["SECRET_HASH"] == "" {
		t.Errorf("unexpected input %+v", in)
	}

	// A challenge (no AuthenticationResult) is unexpected for this pool.
	stub.result = nil
	if _, err := c.Login(context.Background(), "diego@example.com", "x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("challenge: got %v", err)
	}
}

func TestRefreshUsesClientSecretAndDropsRefreshToken(t *testing.T) {
	stub := &stubAPI{result: &types.AuthenticationResultType{
		AccessToken: aws.String("at2"), IdToken: aws.String("it2"), RefreshToken: aws.String("rt"), ExpiresIn: 3600,
	}}
	c := New(stub, testClientID, testSecret)
	tok, err := c.Refresh(context.Background(), "rt")
	if err != nil {
		t.Fatal(err)
	}
	in := stub.last.(*cip.GetTokensFromRefreshTokenInput)
	if aws.ToString(in.ClientSecret) != testSecret || aws.ToString(in.RefreshToken) != "rt" {
		t.Errorf("unexpected input %+v", in)
	}
	if tok.AccessToken != "at2" || tok.RefreshToken != "" {
		t.Errorf("tokens = %+v", tok)
	}
}

func TestGetUser(t *testing.T) {
	stub := &stubAPI{attrs: []types.AttributeType{
		{Name: aws.String("sub"), Value: aws.String("6f1c")},
		{Name: aws.String("email"), Value: aws.String("diego@example.com")},
		{Name: aws.String("name"), Value: aws.String("Diego")},
		{Name: aws.String("email_verified"), Value: aws.String("true")},
	}}
	info, err := New(stub, testClientID, testSecret).GetUser(context.Background(), "at")
	if err != nil {
		t.Fatal(err)
	}
	if info != (UserInfo{Sub: "6f1c", Email: "diego@example.com", DisplayName: "Diego"}) {
		t.Errorf("info = %+v", info)
	}
}
