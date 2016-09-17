package cloudup

import (
	"encoding/json"
	"fmt"
	"github.com/golang/glog"
	"k8s.io/kops/upup/pkg/api"
	"k8s.io/kops/upup/pkg/fi/vfs"
	"strings"
)

const IAMPolicyDefaultVersion = "2012-10-17"

type IAMPolicy struct {
	Version   string
	Statement []*IAMStatement
}

func (p *IAMPolicy) AsJSON() (string, error) {
	j, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", fmt.Errorf("error marshaling policy to JSON: %v", err)
	}
	return string(j), nil
}

type IAMStatementEffect string

const IAMStatementEffectAllow IAMStatementEffect = "Allow"

type IAMStatement struct {
	Effect   IAMStatementEffect
	Action   []string
	Resource []string
}

type IAMPolicyBuilder struct {
	Cluster *api.Cluster
	Role    api.InstanceGroupRole
	Region  string
}

func (b *IAMPolicyBuilder) BuildAWSIAMPolicy() (*IAMPolicy, error) {
	iamPrefix := b.IAMPrefix()

	p := &IAMPolicy{
		Version: IAMPolicyDefaultVersion,
	}

	if b.Role == api.InstanceGroupRoleNode {
		p.Statement = append(p.Statement, &IAMStatement{
			Effect:   IAMStatementEffectAllow,
			Action:   []string{"ec2:Describe*"},
			Resource: []string{"*"},
		})

		// No longer needed in 1.3
		//p.Statement = append(p.Statement, &IAMStatement{
		//	Effect: IAMStatementEffectAllow,
		//	Action: []string{ "ec2:AttachVolume" },
		//	Resource: []string{"*"},
		//})
		//p.Statement = append(p.Statement, &IAMStatement{
		//	Effect: IAMStatementEffectAllow,
		//	Action: []string{ "ec2:DetachVolume" },
		//	Resource: []string{"*"},
		//})

		p.Statement = append(p.Statement, &IAMStatement{
			Effect:   IAMStatementEffectAllow,
			Action:   []string{"route53:*"},
			Resource: []string{"*"},
		})

		p.Statement = append(p.Statement, &IAMStatement{
			Effect: IAMStatementEffectAllow,
			Action: []string{
				"ecr:GetAuthorizationToken",
				"ecr:BatchCheckLayerAvailability",
				"ecr:GetDownloadUrlForLayer",
				"ecr:GetRepositoryPolicy",
				"ecr:DescribeRepositories",
				"ecr:ListImages",
				"ecr:BatchGetImage",
			},
			Resource: []string{"*"},
		})
	}

	if b.Role == api.InstanceGroupRoleMaster {
		p.Statement = append(p.Statement, &IAMStatement{
			Effect:   IAMStatementEffectAllow,
			Action:   []string{"ec2:*"},
			Resource: []string{"*"},
		})

		p.Statement = append(p.Statement, &IAMStatement{
			Effect:   IAMStatementEffectAllow,
			Action:   []string{"route53:*"},
			Resource: []string{"*"},
		})

		p.Statement = append(p.Statement, &IAMStatement{
			Effect:   IAMStatementEffectAllow,
			Action:   []string{"elasticloadbalancing:*"},
			Resource: []string{"*"},
		})
	}

	// For S3 IAM permissions, we grant permissions to subtrees.  So find the parents;
	// we don't need to grant mypath and mypath/child.
	var roots []string
	{
		var locations []string

		for _, p := range []string{
			b.Cluster.Spec.KeyStore,
			b.Cluster.Spec.SecretStore,
			b.Cluster.Spec.ConfigStore,
		} {
			if p == "" {
				continue
			}

			if !strings.HasSuffix(p, "/") {
				p = p + "/"
			}
			locations = append(locations, p)
		}

		for i, l := range locations {
			isTopLevel := true
			for j := range locations {
				if i == j {
					continue
				}
				if strings.HasPrefix(l, locations[j]) {
					glog.V(4).Infof("Ignoring location %q because found parent %q", l, locations[j])
					isTopLevel = false
				}
			}
			if isTopLevel {
				glog.V(4).Infof("Found root location %q", l)
				roots = append(roots, l)
			}
		}
	}

	for _, root := range roots {
		vfsPath, err := vfs.Context.BuildVfsPath(root)
		if err != nil {
			return nil, fmt.Errorf("cannot parse VFS path %q: %v", root, err)
		}

		if s3Path, ok := vfsPath.(*vfs.S3Path); ok {
			// Note that the config store may itself be a subdirectory of a bucket
			iamS3Path := s3Path.Bucket() + "/" + s3Path.Key()
			iamS3Path = strings.TrimSuffix(iamS3Path, "/")

			p.Statement = append(p.Statement, &IAMStatement{
				Effect: IAMStatementEffectAllow,
				Action: []string{"s3:*"},
				Resource: []string{
					iamPrefix + ":s3:::" + iamS3Path,
					iamPrefix + ":s3:::" + iamS3Path + "/*",
				},
			})

			p.Statement = append(p.Statement, &IAMStatement{
				Effect: IAMStatementEffectAllow,
				Action: []string{"s3:GetBucketLocation", "s3:ListBucket"},
				Resource: []string{
					iamPrefix + ":s3:::" + s3Path.Bucket(),
				},
			})
		} else {
			// We could implement this approach, but it seems better to get all clouds using cluster-readable storage
			return nil, fmt.Errorf("path is not cluster readable: %v", root)
		}
	}

	return p, nil
}

// IAMPrefix returns the prefix for AWS ARNs in the current region, for use with IAM
// it is arn:aws everywhere but in cn-north, where it is arn:aws-cn
func (b *IAMPolicyBuilder) IAMPrefix() string {
	switch b.Region {
	case "cn-north-1":
		return "arn:aws-cn"
	default:
		return "arn:aws"
	}
}