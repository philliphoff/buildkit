package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

const (
	DefaultLocalNameContext    = "context"
	DefaultLocalNameDockerfile = "dockerfile"

	NetAppDefaultConfiguration = "Release"
	NetAppSdkImage             = "mcr.microsoft.com/dotnet/sdk:5.0"
	NetAspNetRuntimeImage      = "mcr.microsoft.com/dotnet/aspnet:5.0"

	NetAppDir                  = "/app"
	NetAppMetaDir              = "/meta"
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

// NetAppMetadata Format of metadata extracted from .NET Core project
type NetAppMetadata struct {
	Assembly string
}

// Build Builds a .NET Core Docker-equivalent image
func Build(ctx context.Context, c client.Client) (*client.Result, error) {
	buildOpts := c.BuildOpts()
	opts := buildOpts.Opts

	localNameContext := DefaultLocalNameContext
	if v, ok := opts[keyNameContext]; ok {
		localNameContext = v
	}

	contextSource := llb.Local(localNameContext,
		llb.SessionID(c.BuildOpts().SessionID))

	netAppDockerfile, err := getManifest(ctx, c, opts, buildOpts.SessionID)

	if err != nil {
		return nil, err
	}

	project := getProject(netAppDockerfile, opts)

	if project == "" {
		project, err = inferProject(ctx, c, opts, buildOpts.SessionID)

		if err != nil {
			return nil, err
		}
	}

	configuration := getConfiguration(netAppDockerfile, opts)

	buildDir := path.Join(NetAppDir, "build")
	publishDir := path.Join(NetAppDir, "publish")

	sourceOp := llb.
		Image(NetAppSdkImage).
		Dir(NetAppSourceDir).
		With(
			copyFrom(contextSource, project, "./"),
		).
		Run(llb.Shlexf("dotnet restore \"%s\"", project)).
		With(
			copyAll(contextSource, "."),
		)

	assembly, err := getAssembly(ctx, netAppDockerfile, opts, c, sourceOp, project)

	if err != nil {
		return nil, err
	}

	buildOp := sourceOp.
		Run(llb.Shlexf("dotnet build \"%s\" -c \"%s\" -o \"%s\"", project, configuration, buildDir))

	publishOp := buildOp.
		Run(llb.Shlexf("dotnet publish \"%s\" -c \"%s\" -o \"%s\"", project, configuration, publishDir))

	finalOp := llb.
		Image(NetAspNetRuntimeImage).
		Dir(NetAppDir).
		With(
			copyFrom(publishOp.State, publishDir, "."),
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

func mkDir(destPath string) llb.StateOption {
	return func(dest llb.State) llb.State {
		return dest.File(llb.Mkdir(destPath, 0600))
	}
}

func write(content []byte, destPath string) llb.StateOption {
	return func(dest llb.State) llb.State {
		return dest.File(llb.Mkfile(destPath, 0600, content))
	}
}

func getAssembly(ctx context.Context, manifest NetAppDockerfile, opts map[string]string, c client.Client, sourceOp llb.State, project string) (string, error) {
	assembly := manifest.Assembly

	if assemblyOption, ok := opts[keyNameAssembly]; ok {
		assembly = assemblyOption
	}

	if (assembly == "") {
		targetsContent := 
`<Project>
	<!-- All the relevant info is in root-level PropertyGroups, so there are no dependent targets to make this work -->
	<Target Name="GetProjectProperties">
		<WriteLinesToFile
			File="$(InfoOutputPath)"
			Lines="assembly: &quot;$(AssemblyName).dll&quot;"
			Overwrite="True" />
	</Target>
</Project>`

		targetsFilename := fmt.Sprintf("%s/GetProjectProperties.targets", NetAppMetaDir)
		metadataFilename := fmt.Sprintf("%s/meta.out", NetAppDir)

		metadataOp := sourceOp.
			With(
				mkDir("/meta"),
				write([]byte(targetsContent), targetsFilename),
			).
			Run(llb.Shlexf("dotnet build /t:GetProjectProperties /p:CustomAfterMicrosoftCommonTargets=\"%s\" /p:CustomAfterMicrosoftCommonCrossTargetingTargets=\"%s\" /p:InfoOutputPath=\"%s\" \"%s\"", targetsFilename, targetsFilename, metadataFilename, project))

		metadataOpMarshaled, err := metadataOp.Marshal(ctx, llb.LinuxAmd64)

		if err != nil {
			return "", errors.Wrap(err, "failed to marshal metadata operation")
		}

		metadataOpResult, err := c.Solve(ctx, client.SolveRequest{
			Definition: metadataOpMarshaled.ToPB(),
		})

		if err != nil {
			return "", errors.Wrap(err, "failed to solve metadata operation")
		}

		metadataOpRef, err := metadataOpResult.SingleRef()

		if err != nil {
			return "", errors.Wrap(err, "failed to get metadata reference")
		}

		metadataContent, err := metadataOpRef.ReadFile(ctx, client.ReadRequest{
			Filename: metadataFilename,
		})

		if err != nil {
			return "", errors.Wrap(err, "failed to read metadata content")
		}

		var metadata NetAppMetadata

		if err = yaml.Unmarshal(metadataContent, &metadata); err != nil {
			return "", errors.Wrap(err, "failed to unmarshal metadata")
		}

		assembly = metadata.Assembly
	}

	if assembly == "" {
		return "", errors.New("unable to determine the assembly")
	}

	return assembly, nil
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

func inferProject(ctx context.Context, c client.Client, opts map[string]string, sessionID string) (string, error) {
	localNameDockerfile := DefaultLocalNameDockerfile
	if v, ok := opts[keyNameDockerfile]; ok {
		localNameDockerfile = v
	}

	dockerfileSource := llb.Local(localNameDockerfile,
		llb.SessionID(sessionID),
	)

	dockerfileSourceDefinition, err := dockerfileSource.Marshal(ctx)

	if err != nil {
		return "", errors.Wrap(err, "failed to marshal Dockerfile source")
	}

	dockerfileSourceResult, err := c.Solve(ctx, client.SolveRequest{
		Definition: dockerfileSourceDefinition.ToPB(),
	})

	if err != nil {
		return "", errors.Wrap(err, "failed to resolve Dockerfile source")
	}

	dockerfileSourceRef, err := dockerfileSourceResult.SingleRef()

	if err != nil {
		return "", errors.Wrap(err, "failed to obtain reference to Dockerfile source")
	}

	readDirResult, err := dockerfileSourceRef.ReadDir(ctx, client.ReadDirRequest{
		Path: ".",
		IncludePattern: "*.*proj",
	})

	if err != nil {
		return "", errors.Wrap(err, "failed to read the Dockerfile directory")
	}

	for _, file := range readDirResult {
		if os.FileMode(file.Mode).IsRegular() {
			return file.Path, nil
		}
	}

	logrus.Info("Hello world!")

	return "", errors.New("no project could be inferred")
}

func getManifest(ctx context.Context, c client.Client, opts map[string]string, sessionID string) (NetAppDockerfile, error) {
	var manifest NetAppDockerfile

	localNameDockerfile := DefaultLocalNameDockerfile
	if v, ok := opts[keyNameDockerfile]; ok {
		localNameDockerfile = v
	}

	filename := opts[keyFilename]

	if filename == "" {
		filename = defaultDockerfileName
	}

	filenames := []string{filename}

	dockerfileSource := llb.Local(localNameDockerfile,
		llb.FollowPaths(filenames),
		llb.SessionID(sessionID),
	)

	dockerfileSourceDefinition, err := dockerfileSource.Marshal(ctx)

	if err != nil {
		return manifest, errors.Wrap(err, "failed to marshal Dockerfile source")
	}

	dockerfileSourceResult, err := c.Solve(ctx, client.SolveRequest{
		Definition: dockerfileSourceDefinition.ToPB(),
	})

	if err != nil {
		return manifest, errors.Wrap(err, "failed to resolve Dockerfile source")
	}

	dockerfileSourceRef, err := dockerfileSourceResult.SingleRef()

	if err != nil {
		return manifest, errors.Wrap(err, "failed to obtain reference to Dockerfile source")
	}

	dockerfileBytes, err := dockerfileSourceRef.ReadFile(ctx, client.ReadRequest{
		Filename: filename,
	})

	if err != nil {
		return manifest, errors.Wrap(err, "failed to read Dockerfile source")
	}

	err = yaml.Unmarshal(dockerfileBytes, &manifest)

	if err != nil {
		return manifest, errors.Wrap(err, "failed to marshal Dockerfile object")
	}

	return manifest, nil
}

func getProject(manifest NetAppDockerfile, opts map[string]string) string {
	project := manifest.Project

	if projectOption, ok := opts[keyNameProject]; ok {
		project = projectOption
	}

	return project
}
