package main

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ssm"
)

// LambdaFunc contains all the information about the lambda
type LambdaFunc struct {
	ProjectID  string
	Parameters []*ssm.Parameter
	SSM        *ssm.SSM
}

func (l *LambdaFunc) getParameters() ([]*ssm.Parameter, error) {
	var err error

	l.Parameters = make([]*ssm.Parameter, 0)
	l.Parameters, err = l.getSSMParameters(true, true, nil)
	if err != nil {
		return nil, err
	}

	return l.Parameters, err
}

func (l *LambdaFunc) getSSMParameters(recursive bool, withDecryption bool, nextToken *string) ([]*ssm.Parameter, error) {
	var err error

	params := &ssm.GetParametersByPathInput{
		Path:           aws.String(fmt.Sprintf("/%s", l.ProjectID)),
		WithDecryption: aws.Bool(withDecryption),
	}

	fmt.Println(params.Path)

	output, err := l.SSM.GetParametersByPath(params)
	if err != nil {
		return l.Parameters, err
	}

	l.Parameters = append(l.Parameters, output.Parameters...)

	if nextToken != nil {
		l.getSSMParameters(recursive, withDecryption, nextToken)
	}

	return l.Parameters, err
}
