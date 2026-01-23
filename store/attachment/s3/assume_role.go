package s3

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// newAssumeRoleProvider creates a credentials provider that assumes the given role.
func newAssumeRoleProvider(cfg aws.Config, roleARN, sessionName, externalID string) aws.CredentialsProvider {
	stsClient := sts.NewFromConfig(cfg)

	opts := func(o *stscreds.AssumeRoleOptions) {
		if sessionName != "" {
			o.RoleSessionName = sessionName
		}
		if externalID != "" {
			o.ExternalID = aws.String(externalID)
		}
	}

	return stscreds.NewAssumeRoleProvider(stsClient, roleARN, opts)
}
