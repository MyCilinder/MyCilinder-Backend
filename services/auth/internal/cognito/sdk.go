package cognito

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
)

// NewSDKClient builds the Cognito SDK client for region.
//
// The operations the facade uses (SignUp, ConfirmSignUp, ResendConfirmationCode,
// InitiateAuth, GetTokensFromRefreshToken, RevokeToken, GetUser) are authorized by the app
// client secret and user tokens, not IAM (Architecture §4.4), so requests are sent
// unsigned. No AWS credentials are needed locally, and the task role needs no Cognito
// permissions.
//
// Each call is bounded to 5 seconds so a slow Cognito cannot hold a request past the
// server's write timeout.
func NewSDKClient(ctx context.Context, region string) (*cip.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(aws.AnonymousCredentials{}),
		awsconfig.WithRetryMaxAttempts(2),
		awsconfig.WithHTTPClient(awshttp.NewBuildableClient().WithTimeout(5*time.Second)),
	)
	if err != nil {
		return nil, fmt.Errorf("cognito: load aws config: %w", err)
	}
	return cip.NewFromConfig(cfg), nil
}
