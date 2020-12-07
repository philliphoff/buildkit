package builder

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
)

const (
	DefaultLocalNameContext    = "context"
	DefaultLocalNameDockerfile = "dockerfile"

	defaultDockerfileName      = "Dockerfile"
	keyFilename                = "filename"
	keyNameAssembly            = "assembly"
	keyNameContext             = "contextkey"
	keyNameDockerfile          = "dockerfilekey"
	keyNameProject             = "project"
)

// NetAppDockerfile Format of .NET Core "Dockerfile"
type NetAppDockerfile struct {
	Assembly string
	Project string
}

// Build Builds a .NET Core Docker-equivalent image
func Build(ctx context.Context, c client.Client) (*client.Result, error) {
	opts := c.BuildOpts().Opts

	localNameContext := DefaultLocalNameContext
	if v, ok := opts[keyNameContext]; ok {
		localNameContext = v
	}

	contextSource := llb.Local(localNameContext,
		llb.SessionID(c.BuildOpts().SessionID))

	localNameDockerfile := DefaultLocalNameDockerfile
	if v, ok := opts[keyNameDockerfile]; ok {
		localNameDockerfile = v
	}

	filename := opts[keyFilename]

	if filename == "" {
		return nil, errors.New("failed to get Dockerfile filename option")
	}

	if filename == "" {
		filename = defaultDockerfileName
	}

	filenames := []string{filename}

	dockerfileSource := llb.Local(localNameDockerfile,
		llb.FollowPaths(filenames),
		llb.SessionID(c.BuildOpts().SessionID),
	)

	dockerfileSourceDefinition, err := dockerfileSource.Marshal(ctx)

	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal Dockerfile source")
	}

	dockerfileSourceResult, err := c.Solve(ctx, client.SolveRequest{
		Definition: dockerfileSourceDefinition.ToPB(),
	})

	if err != nil {
		return nil, errors.Wrap(err, "failed to resolve Dockerfile source")
	}

	dockerfileSourceRef, err := dockerfileSourceResult.SingleRef()

	if err != nil {
		return nil, errors.Wrap(err, "failed to obtain reference to Dockerfile source")
	}

	dockerfileBytes, err := dockerfileSourceRef.ReadFile(ctx, client.ReadRequest{
		Filename: filename,
	})

	if err != nil {
		return nil, errors.Wrap(err, "failed to read Dockerfile source")
	}

	if len(dockerfileBytes) == 0 {
		return nil, errors.New("file is zero bytes")
	}

	netAppDockerfile := NetAppDockerfile{}

	err = yaml.Unmarshal(dockerfileBytes, &netAppDockerfile)

	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal Dockerfile object")
	}
	
	assembly := netAppDockerfile.Assembly
	project := netAppDockerfile.Project

	if project == "" {
		return nil, errors.New(fmt.Sprintf("failed to get project from Dockerfile \"%s\"", filename))
	}

	if assemblyOption, ok := opts[keyNameAssembly]; ok {
		assembly = assemblyOption
	}

	if projectOption, ok := opts[keyNameProject]; ok {
		project = projectOption
	}

	sourceOp := llb.
		Image("mcr.microsoft.com/dotnet/sdk:5.0").
		Dir("/src").
		With(
			copyFrom(contextSource, project, "./"),
		).
		Run(llb.Shlexf("dotnet restore \"%s\"", project)).
		With(
			copyAll(contextSource, "."),
		).
		Run(llb.Shlexf("dotnet build \"%s\" -c Release -o /app/build", project))

	publishOp := sourceOp.
		Run(llb.Shlexf("dotnet publish \"%s\" -c Release -o /app/publish", project))

	finalOp := llb.
		Image("mcr.microsoft.com/dotnet/aspnet:5.0").
		Dir("/app").
		With(
			copyFrom(publishOp.State, "/app/publish", "."),
		)

	dt, err := finalOp.Marshal(ctx, llb.LinuxAmd64)

	if err != nil {
		return nil, errors.Wrap(err, "Unable to create definition")
	}

	res, err := c.Solve(ctx, client.SolveRequest{
		Definition: dt.ToPB(),
	})

	if err != nil {
		return nil, errors.Wrap(err, "Unable to solve the definition")
	}

	buildRes := client.NewResult()

	ref, err := res.SingleRef()

	if err != nil {
		return nil, errors.Wrap(err, "Unable to get reference.")
	}

	image := dockerfile2llb.Image{
	}

	var env []string

	// TODO: Pull from base image.
	env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	env = append(env, "ASPNETCORE_URLS=http://+:80")
	env = append(env, "DOTNET_RUNNING_IN_CONTAINER=true")
	env = append(env, "DOTNET_VERSION=5.0.0")
	env = append(env, "ASPNET_VERSION=5.0.0")

	var entrypoint []string

	entrypoint = append(entrypoint, "dotnet")
	// TODO: Pull/evaluate from project file
	entrypoint = append(entrypoint, assembly)

	image.Architecture = "amd64"
	image.Config.Entrypoint = entrypoint
	image.Config.Env = env

	if image.Config.ExposedPorts == nil {
		image.Config.ExposedPorts = make(map[string]struct{})
	}

	image.Config.ExposedPorts["80/tcp"] = struct{}{}

	image.Config.WorkingDir = "/app"

	imageMarshaled, err := json.Marshal(image)

	if err != nil {
		return nil, errors.Wrap(err, "Unable to marshal the image metadata")
	}

	buildRes.AddMeta(exptypes.ExporterImageConfigKey, imageMarshaled)
	buildRes.SetRef(ref)

	return buildRes, nil
}

func copyAll(src llb.State, destPath string) llb.StateOption {
	return copyFrom(src, "/.", destPath)
}

// copyFrom has similar semantics as `COPY --from`
func copyFrom(src llb.State, srcPath, destPath string) llb.StateOption {
	return func(s llb.State) llb.State {
		return copy(src, srcPath, s, destPath)
	}
}

// copy copies files between 2 states using cp
func copy(src llb.State, srcPath string, dest llb.State, destPath string) llb.State {
	return dest.File(llb.Copy(src, srcPath, destPath, &llb.CopyInfo{
		AllowWildcard:  true,
		AttemptUnpack:  true,
		CreateDestPath: true,
	}))
}
