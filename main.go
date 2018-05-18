package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	l "github.com/axelspringer/vodka-aws/lambda"
	event "github.com/eawsy/aws-lambda-go-event/service/lambda/runtime/event/codepipelineevt"
	log "github.com/sirupsen/logrus"
)

const (
	defaultEnvProjectID = "PROJECT_ID"
	defaultTimeout      = 60
)

var (
	errNoProjectID = errors.New("no ProjectID present")

	parameters = []string{"ecs-cluster"}
)

// Handler is executed by AWS Lambda in the main function. Once the request
// is processed, it returns an Amazon API Gateway response object to AWS Lambda
func Handler(ctx context.Context, event event.Event) error {
	var err error

	withTimeout, cancel := context.WithTimeout(ctx, defaultTimeout*time.Second)
	defer cancel()

	deploy, err := NewDeploy(withTimeout, event.Job)
	if err != nil {
		return err
	}

	projectID, ok := os.LookupEnv(defaultEnvProjectID)
	if !ok {
		err = deploy.putJobFailure(NewFailure(err))
		return errNoProjectID
	}

	lambdaFunc := l.New(projectID)
	if _, err := lambdaFunc.Store.TestEnv(parameters); err != nil {
		err = deploy.putJobFailure(NewFailure(err))
		return err
	}

	env, err := lambdaFunc.Store.GetEnv()
	if err != nil {
		err = deploy.putJobFailure(NewFailure(err))
		return err
	}

	deploy.ECSCluster = env["ecs-cluster"]

	if err = deploy.updateServices(); err != nil {
		return deploy.putJobFailure(NewFailure(err))
	}

	err = deploy.putJobSuccess(nil)

	return err // noop
}

func main() {
	log.SetFormatter(&log.JSONFormatter{})

	lambda.Start(Handler)
}
