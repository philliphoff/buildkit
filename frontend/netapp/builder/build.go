package builder

import (
	"context"
	"encoding/json"

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

	NetAppDefaultConfiguration = "Release"
	NetAppSdkImage             = "mcr.microsoft.com/dotnet/sdk:5.0"
	NetAspNetRuntimeImage      = "mcr.microsoft.com/dotnet/aspnet:5.0"

	NetAppDir                  = "/app"
	NetAppSourceDir            = "/src"

	defaultDockerfileName      = "Dockerfile"
	keyFilename                = "filename"
	keyNameAssembly            = "assembly"
	keyNameConfiguration       = "configuration"
	keyNameContext             = "contextkey"
	keyNameDockerfile          = "dockerfilekey"
	keyNameProject             = "project"
)

// NetAppDockerfile Format of .NET Core "Dockerfile"
type NetAppDockerfile struct {
	Assembly string
	Configuration string
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

	project, err := getProject(netAppDockerfile, opts)

	if err != nil {
		return nil, err
	}

	assembly, err := getAssembly(netAppDockerfile, opts)

	if err != nil {
		return nil, err
	}

	configuration := getConfiguration(netAppDockerfile, opts)

	sourceOp := llb.
		Image(NetAppSdkImage).
		Dir(NetAppSourceDir).
		With(
			copyFrom(contextSource, project, "./"),
		).
		Run(llb.Shlexf("dotnet restore \"%s\"", project)).
		With(
			copyAll(contextSource, "."),
		).
		Run(llb.Shlexf("dotnet build \"%s\" -c \"%s\" -o %s/build", project, configuration, NetAppDir))

	publishOp := sourceOp.
		Run(llb.Shlexf("dotnet publish \"%s\" -c \"%s\" -o %s/publish", project, configuration, NetAppDir))

	finalOp := llb.
		Image(NetAspNetRuntimeImage).
		Dir(NetAppDir).
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

	var entrypoint []string

	entrypoint = append(entrypoint, "dotnet")
	// TODO: Pull/evaluate from project file
	entrypoint = append(entrypoint, assembly)

	image.Architecture = "amd64"
	image.Config.Entrypoint = entrypoint

	_, bytes, err := c.ResolveImageConfig(ctx, NetAspNetRuntimeImage, llb.ResolveImageConfigOpt{})

	if err != nil {
		return nil, errors.Wrap(err, "failed to runtime resolve image config")
	}

	runtimeImageConfig := dockerfile2llb.Image{}

	if err := json.Unmarshal(bytes, &runtimeImageConfig); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal runtime image config")
	}

	image.Config.Env = runtimeImageConfig.Config.Env

	if image.Config.ExposedPorts == nil {
		image.Config.ExposedPorts = make(map[string]struct{})
	}

	image.Config.ExposedPorts["80/tcp"] = struct{}{}

	image.Config.WorkingDir = NetAppDir

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

func getAssembly(manifest NetAppDockerfile, opts map[string]string) (string, error) {
	assembly := manifest.Assembly

	if assemblyOption, ok := opts[keyNameAssembly]; ok {
		assembly = assemblyOption
	}

	var error error

	if assembly == "" {
		error = errors.New("no assembly property or option was set")
	}

	return assembly, error
}

func getConfiguration(manifest NetAppDockerfile, opts map[string]string) string {
	configuration := manifest.Configuration

	if configurationOption, ok := opts[keyNameConfiguration]; ok {
		configuration = configurationOption
	}

	if configuration == "" {
		configuration = NetAppDefaultConfiguration
	}

	return configuration
}

func getProject(manifest NetAppDockerfile, opts map[string]string) (string, error) {
	project := manifest.Project

	if projectOption, ok := opts[keyNameProject]; ok {
		project = projectOption
	}

	var error error

	if project == "" {
		error = errors.New("no project property or option was set")
	}

	return project, error
}
