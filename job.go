package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bradfitz/slice"
	log "github.com/sirupsen/logrus"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/codepipeline"
	"github.com/aws/aws-sdk-go/service/codepipeline/codepipelineiface"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/eawsy/aws-lambda-go-event/service/lambda/runtime/event/codepipelineevt"
	event "github.com/eawsy/aws-lambda-go-event/service/lambda/runtime/event/codepipelineevt"
)

const (
	tmpFolder     = "/tmp"
	svcDefinition = "imagedefinitions.json"
)

// Deploy contains all the functionality to deploy services to ECS clusters
type Deploy struct {
	ECSCluster string
	Job        *codepipelineevt.Job
	pipe       codepipelineiface.CodePipelineAPI
	session    *session.Session
	s3         *s3.S3
	ecs        *ecs.ECS
	ctx        context.Context
	Services   Services
}

// Service contains a service update
type Service struct {
	ServiceName      string            `json:"ServiceName"`
	ImageDefinitions []ImageDefinition `json:"ImageDefinitions"`
}

// ImageDefinition is the specifiction of an image to be updated
type ImageDefinition struct {
	Name     string `json:"name"`
	ImageURI string `json:"imageUri"`
}

// Services contains other services subject to be updates
type Services []*Service

// NewDeploy returns a new deployment structure
func NewDeploy(ctx context.Context, job *event.Job) (*Deploy, error) {
	var err error

	sess := session.New()

	deploy := new(Deploy)
	deploy.Job = job
	deploy.session = sess
	deploy.ctx = ctx

	s3AccessKeyID := job.Data.ArtifactCredentials.AccessKeyID
	s3SecretAccessKey := job.Data.ArtifactCredentials.SecretAccessKey
	s3SessionToken := job.Data.ArtifactCredentials.SessionToken

	deploy.pipe = codepipeline.New(sess)
	deploy.s3 = s3.New(session.Must(session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(s3AccessKeyID, s3SecretAccessKey, s3SessionToken),
	})))
	deploy.ecs = ecs.New(sess)

	svcs, err := deploy.getServiceDefinition()
	if err != nil {
		return nil, err
	}

	slice.Sort(svcs, func(i, j int) bool {
		return svcs[i].ServiceName < svcs[j].ServiceName
	})

	deploy.Services = svcs

	return deploy, err
}

// NewFailure returns a new pipeline failure of the given err
func NewFailure(err error) *codepipeline.FailureDetails {
	return &codepipeline.FailureDetails{
		Message: aws.String(err.Error()),
		Type:    aws.String(codepipeline.FailureTypeJobFailed),
	}
}

// NewExecDetails return new pipeline execution details
func NewExecDetails() *codepipeline.ExecutionDetails {
	return &codepipeline.ExecutionDetails{
	// should do more
	}
}

func (d *Deploy) updateServices() error {
	var err error

	svcs, err := d.describeServices()
	if err != nil {
		return err
	}

	for _, svc := range svcs {
		pos := sort.Search(len(d.Services), func(i int) bool { return aws.StringValue(svc.ServiceName) <= d.Services[i].ServiceName })
		if len(d.Services) == pos {
			continue
		}
		imageDefinitions := d.Services[pos].ImageDefinitions

		task, err := d.describeTaskDefinition(svc.TaskDefinition)
		if err != nil {
			return err
		}

		for _, imageDefinition := range imageDefinitions {
			pos := sort.Search(len(task.ContainerDefinitions), func(i int) bool { return imageDefinition.Name <= aws.StringValue(task.ContainerDefinitions[i].Name) })
			if len(task.ContainerDefinitions) == pos {
				return fmt.Errorf("could not find task %v", imageDefinition.Name)
			}
			task.ContainerDefinitions[pos].SetImage(imageDefinition.ImageURI)
		}

		newTask, err := d.registerTaskDefinition(task)
		if err != nil {
			return err
		}

		input := &ecs.UpdateServiceInput{
			Cluster:                       svc.ClusterArn,
			TaskDefinition:                newTask.TaskDefinitionArn,
			DeploymentConfiguration:       svc.DeploymentConfiguration,
			DesiredCount:                  svc.DesiredCount,
			HealthCheckGracePeriodSeconds: svc.HealthCheckGracePeriodSeconds,
			NetworkConfiguration:          svc.NetworkConfiguration,
			Service:                       svc.ServiceName,
			// NewDeployment:            aws.Bool(true),
		}

		_, err = d.ecs.UpdateServiceWithContext(d.ctx, input)
		if err != nil {
			return err
		}
	}

	return err
}

func (d *Deploy) getServiceDefinition() (Services, error) {
	var err error
	var svcs Services

	tmpDir, err := ioutil.TempDir("/tmp", "ecs-deploy")
	if err != nil {
		return nil, err
	}

	defer os.RemoveAll(tmpDir)

	files, err := downloadArtifacts(d.s3, d.Job.Data.InputArtifacts, tmpDir)
	if err != nil {
		return nil, err
	}

	sort.Strings(files)
	pos := sort.Search(len(files), func(i int) bool { return strings.Contains(files[i], svcDefinition) })
	if pos == len(files) {
		return svcs, fmt.Errorf("could not find %v", svcDefinition)
	}

	data, err := ioutil.ReadFile(files[pos])
	if err != nil {
		return svcs, fmt.Errorf("could not read definition: %v", err)
	}

	err = json.Unmarshal(data, &svcs)
	log.WithFields(log.Fields{
		"ServiceDefinitionJSON from artifact": string(data),
	}).Info("ServiceDefinitionJSON output")

	return svcs, err
}

func (d *Deploy) putJobSuccess(execDetails *codepipeline.ExecutionDetails) error {
	var err error

	input := &codepipeline.PutJobSuccessResultInput{
		JobId:            aws.String(d.Job.ID),
		ExecutionDetails: execDetails,
	}

	_, err = d.pipe.PutJobSuccessResult(input)

	return err
}

func (d *Deploy) putJobFailure(failure *codepipeline.FailureDetails) error {
	var err error

	input := &codepipeline.PutJobFailureResultInput{
		JobId:          aws.String(d.Job.ID),
		FailureDetails: failure,
	}

	_, err = d.pipe.PutJobFailureResult(input)

	return err
}

func downloadArtifacts(client *s3.S3, artifcats []*event.Artifact, tmpDir string) ([]string, error) {
	var err error
	var zips []string
	var filenames []string

	for _, artifact := range artifcats {
		filename, err := download(client, artifact.Location.S3Location.BucketName, artifact.Location.S3Location.ObjectKey, tmpDir)
		if err != nil {
			return filenames, err
		}
		zips = append(zips, filename)
	}

	for _, zip := range zips {
		files, err := unzip(zip, tmpDir)
		if err != nil {
			return filenames, err
		}

		filenames = append(filenames, files...)
	}

	return filenames, err
}

func download(client *s3.S3, bucket string, key string, dest string) (string, error) {
	var fPath string

	object, err := client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})

	if err != nil {
		return fPath, err
	}

	defer object.Body.Close()

	buf := bytes.NewBuffer(nil)
	if _, err := io.Copy(buf, object.Body); err != nil {
		return fPath, err
	}

	fPath = filepath.Join(dest, key)
	if os.MkdirAll(path.Dir(fPath), os.ModePerm) != nil {
		return fPath, fmt.Errorf("could not create path: %v", err)
	}

	err = ioutil.WriteFile(fPath, buf.Bytes(), os.ModePerm)
	if err != nil {
		return fPath, fmt.Errorf("could not write file: %v", err)
	}

	return fPath, err
}

func (d *Deploy) getServices() []*string {
	var services []*string

	for _, svc := range d.Services {
		services = append(services, &svc.ServiceName)
	}

	return services
}

func (d *Deploy) describeTaskDefinition(taskArn *string) (*ecs.TaskDefinition, error) {
	var err error

	input := &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: taskArn,
	}

	res, err := d.ecs.DescribeTaskDefinitionWithContext(d.ctx, input)

	return res.TaskDefinition, err
}

func (d *Deploy) describeServices() ([]*ecs.Service, error) {
	var err error

	svcs := d.getServices()

	input := &ecs.DescribeServicesInput{
		Cluster:  aws.String(d.ECSCluster),
		Services: svcs,
	}

	res, err := d.ecs.DescribeServicesWithContext(d.ctx, input)

	return res.Services, err
}

func (d *Deploy) registerTaskDefinition(task *ecs.TaskDefinition) (*ecs.TaskDefinition, error) {
	var err error

	input := &ecs.RegisterTaskDefinitionInput{
		ContainerDefinitions:    task.ContainerDefinitions,
		Cpu:                     task.Cpu,
		ExecutionRoleArn:        task.ExecutionRoleArn,
		Family:                  task.Family,
		Memory:                  task.Memory,
		NetworkMode:             task.NetworkMode,
		PlacementConstraints:    task.PlacementConstraints,
		RequiresCompatibilities: task.RequiresCompatibilities,
		TaskRoleArn:             task.TaskRoleArn,
		Volumes:                 task.Volumes,
	}

	res, err := d.ecs.RegisterTaskDefinitionWithContext(d.ctx, input)

	return res.TaskDefinition, err
}

func unzip(src string, dest string) ([]string, error) {
	var filenames []string

	r, err := zip.OpenReader(src)
	if err != nil {
		return filenames, err
	}
	defer r.Close()

	for _, f := range r.File {

		rc, err := f.Open()
		if err != nil {
			return filenames, err
		}
		defer rc.Close()

		// Store filename/path for returning and using later on
		fpath := filepath.Join(dest, f.Name)
		filenames = append(filenames, fpath)

		if f.FileInfo().IsDir() {

			// Make Folder
			os.MkdirAll(fpath, os.ModePerm)

		} else {

			// Make File
			if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
				return filenames, err
			}

			outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return filenames, err
			}

			_, err = io.Copy(outFile, rc)

			// Close the file without defer to close before next iteration of loop
			outFile.Close()

			if err != nil {
				return filenames, err
			}

		}
	}

	return filenames, nil
}
