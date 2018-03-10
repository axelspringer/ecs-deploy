package main

import (
	"errors"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	l "github.com/axelspringer/go-aws/lambda"
)

const (
	defaultEnvProjectID = "PROJECT_ID"
)

var (
	errNoProjectID = errors.New("no ProjectID present")

	parameters = []string{"ecs-cluster"}
)

// Handler is executed by AWS Lambda in the main function. Once the request
// is processed, it returns an Amazon API Gateway response object to AWS Lambda
func Handler(request events.APIGatewayProxyRequest) error {
	var err error

	projectID, ok := os.LookupEnv(defaultEnvProjectID)
	if !ok {
		return errNoProjectID
	}

	lambdaFunc := l.New(projectID)
	if _, err := lambdaFunc.Store.TestEnv(parameters); err != nil {
		return err
	}

	env, err := lambdaFunc.Store.GetEnv()
	if err != nil {
		return err
	}

	deploy := new(Deploy)
	deploy.ECSCluster = env["ecs-cluster"]

	return err
}

func main() {
	lambda.Start(Handler)
}
