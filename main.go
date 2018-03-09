package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ssm"
)

const (
	defaultEnvProjectID = "PROJECT_ID"
)

var (
	errNoProjectID = errors.New("no ProjectID present")
)

// Handler is executed by AWS Lambda in the main function. Once the request
// is processed, it returns an Amazon API Gateway response object to AWS Lambda
func Handler(request events.APIGatewayProxyRequest) error {
	var err error

	projectID, ok := os.LookupEnv(defaultEnvProjectID)
	if !ok {
		return errNoProjectID
	}

	fmt.Println(projectID)

	lambdaFunc := &LambdaFunc{
		ProjectID: projectID,
		SSM:       ssm.New(session.New()),
	}

	parameters, err := lambdaFunc.getParameters()
	if err != nil {
		return err
	}

	for _, parameter := range parameters {
		fmt.Println(aws.StringValue(parameter.Value))
	}

	return err
}

func main() {
	lambda.Start(Handler)
}
