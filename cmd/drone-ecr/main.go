package main

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	ecr "github.com/aws/aws-sdk-go/service/ecrpublic"
)

const defaultRegion = "us-east-1"

func main() {
	// Load env-file if it exists first
	if env := os.Getenv("PLUGIN_ENV_FILE"); env != "" {
		godotenv.Load(env)
	}

	var (
		repo             = getenv("PLUGIN_REPO")
		registry         = getenv("PLUGIN_REGISTRY")
		region           = getenv("PLUGIN_REGION", "ECR_REGION", "AWS_REGION")
		key              = getenv("PLUGIN_ACCESS_KEY", "ECR_ACCESS_KEY", "AWS_ACCESS_KEY_ID")
		secret           = getenv("PLUGIN_SECRET_KEY", "ECR_SECRET_KEY", "AWS_SECRET_ACCESS_KEY")
		create           = parseBoolOrDefault(false, getenv("PLUGIN_CREATE_REPOSITORY", "ECR_CREATE_REPOSITORY"))
		repositoryPolicy = getenv("PLUGIN_REPOSITORY_POLICY")
		assumeRole       = getenv("PLUGIN_ASSUME_ROLE")
		externalId       = getenv("PLUGIN_EXTERNAL_ID")
		scanOnPush       = parseBoolOrDefault(false, getenv("PLUGIN_SCAN_ON_PUSH"))
	)

	// set the region
	if region == "" {
		region = defaultRegion
	}

	os.Setenv("AWS_REGION", region)

	if key != "" && secret != "" {
		os.Setenv("AWS_ACCESS_KEY_ID", key)
		os.Setenv("AWS_SECRET_ACCESS_KEY", secret)
	}

	sess, err := session.NewSession(&aws.Config{Region: &region})
	if err != nil {
		log.Fatal(fmt.Sprintf("error creating aws session: %v", err))
	}

	svc := getECRClient(sess, assumeRole, externalId)
	username, password, err := getAuthInfo(svc)

	if err != nil {
		log.Fatal(fmt.Sprintf("error getting ECR auth: %v", err))
	}

	if create {
		err = ensureRepoExists(svc, repo, scanOnPush)
		if err != nil {
			log.Fatal(fmt.Sprintf("error creating ECR repo: %v", err))
		}
	}

	if repositoryPolicy != "" {
		p, err := ioutil.ReadFile(repositoryPolicy)
		if err != nil {
			log.Fatal(err)
		}
		if err := uploadRepositoryPolicy(svc, string(p), trimHostname(repo, registry)); err != nil {
			log.Fatal(fmt.Sprintf("error uploading ECR repository policy. %v", err))
		}
	}

	os.Setenv("PLUGIN_REPO", repo)
	os.Setenv("PLUGIN_REGISTRY", registry)
	os.Setenv("DOCKER_USERNAME", username)
	os.Setenv("DOCKER_PASSWORD", password)

	// invoke the base docker plugin binary
	cmd := exec.Command("drone-docker")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err = cmd.Run(); err != nil {
		os.Exit(1)
	}
}

func trimHostname(repo, registry string) string {
	repo = strings.TrimPrefix(repo, registry)
	repo = strings.TrimLeft(repo, "/")
	return repo
}

func ensureRepoExists(svc *ecr.ECRPublic, name string, scanOnPush bool) (err error) {
	input := &ecr.CreateRepositoryInput{}
	input.SetRepositoryName(name)
	_, err = svc.CreateRepository(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == ecr.ErrCodeRepositoryAlreadyExistsException {
			// eat it, we skip checking for existing to save two requests
			err = nil
		}
	}

	return
}

func uploadRepositoryPolicy(svc *ecr.ECRPublic, repositoryPolicy string, name string) (err error) {
	input := &ecr.SetRepositoryPolicyInput{}
	input.SetPolicyText(repositoryPolicy)
	input.SetRepositoryName(name)
	_, err = svc.SetRepositoryPolicy(input)

	return err
}

func getAuthInfo(svc *ecr.ECRPublic) (username, password string, err error) {
	var result *ecr.GetAuthorizationTokenOutput
	var decoded []byte

	result, err = svc.GetAuthorizationToken(&ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return
	}

	auth := result.AuthorizationData
	token := *auth.AuthorizationToken
	decoded, err = base64.StdEncoding.DecodeString(token)
	if err != nil {
		return
	}

	// registry = strings.TrimPrefix(*auth.ProxyEndpoint, "https://")
	creds := strings.Split(string(decoded), ":")
	username = creds[0]
	password = creds[1]
	return
}

func parseBoolOrDefault(defaultValue bool, s string) (result bool) {
	var err error
	result, err = strconv.ParseBool(s)
	if err != nil {
		result = false
	}

	return
}

func getenv(key ...string) (s string) {
	for _, k := range key {
		s = os.Getenv(k)
		if s != "" {
			return
		}
	}
	return
}

func getECRClient(sess *session.Session, role string, externalId string) *ecr.ECRPublic {
	if role == "" {
		return ecr.New(sess)
	}
	if externalId != "" {
		return ecr.New(sess, &aws.Config{
			Credentials: stscreds.NewCredentials(sess, role, func(p *stscreds.AssumeRoleProvider) {
				p.ExternalID = &externalId
			}),
		})
	} else {
		return ecr.New(sess, &aws.Config{
			Credentials: stscreds.NewCredentials(sess, role),
		})
	}
}
