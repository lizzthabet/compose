/*
   Copyright 2020 Docker Compose CLI authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package compose

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/compose/v2/pkg/api"
	engineTypes "github.com/docker/docker/api/types"
	"github.com/spf13/cobra"
)

type generateOptions struct {
	*ProjectOptions
}

func generateCommand(p *ProjectOptions, dockerCli command.Cli, backend api.Service) *cobra.Command {
	// This is where command-specific flags get added
	opts := generateOptions{
		ProjectOptions: p,
	}
	cmd := &cobra.Command{
		Use:   "generate [OPTIONS]",
		Short: "EXPERIMENTAL - Generate a compose file from existing container(s)",
		RunE: Adapt(func(ctx context.Context, args []string) error {
			return runGenerate(ctx, dockerCli, backend, &opts, args)
		}),
		Args: cobra.MinimumNArgs(1),
	}
	// Flags for this command get defined here (and added to &opts) with:
	// flags := cmd.Flags()
	// flags.StringVar(...)
	// flags.BoolVar(...)

	return cmd
}

func runGenerate(ctx context.Context, dockerCli command.Cli, backend api.Service, opts *generateOptions, args []string) error {
	_, _ = fmt.Fprintln(os.Stderr, "generate command is EXPERIMENTAL ...and it's running ☞ ☁︎ ☀︎")

	projectName := getName(opts)
	workingDir := getWorkingDir(opts)
	services := map[string]types.ServiceConfig{}

	// This is what we're doing approximately!
	//  => Get the `docker inspect` output from that container
	//  => Translate that data to compose config
	for i, arg := range args {
		container, err := dockerCli.Client().ContainerInspect(ctx, arg)
		if err != nil {
			fmt.Printf("failed to inspect container: %v", err)
			return err
		}
		name := getServiceName(container, i)
		ports, expose := getServiceBindingsPorts(container)
		environment := getServiceEnv(container)
		entrypoint := getServiceEntrypoint(container)
		volumes := getServiceMounts(container)

		// LOL
		image := getServiceImage(container)

		service := types.ServiceConfig{
			Name:        name,
			Image:       image,
			Environment: environment,
			Expose:      expose,
			Entrypoint:  entrypoint,
			Ports:       ports,
			Volumes:     volumes,
		}

		cmd := getServiceCmd(container)
		if len(cmd) > 0 {
			service.Command = cmd
		}

		services[name] = service

		// For printing the container details:
		// jsonContainer, _ := json.Marshal(container)
		// fmt.Printf("here be container:\n%s", string(jsonContainer))
	}

	p := types.Project{
		Name:       projectName,
		WorkingDir: workingDir,
		Services:   services,
	}

	yaml, err := p.MarshalYAML()
	if err != nil {
		fmt.Printf("oops bad %v", err)
	}
	fmt.Printf("\n%s", string(yaml))

	return nil
}

func getName(opts *generateOptions) string {
	if opts.ProjectName != "" {
		return opts.ProjectName
	}

	return ""
}

func getWorkingDir(opts *generateOptions) string {
	if opts.WorkDir != "" {
		return opts.WorkDir
	}

	currentWorkDir, err := os.Getwd()
	if err != nil {
		fmt.Printf("warning: unable to get working directory %v", err)
		return ""
	}

	return currentWorkDir
}

func getServiceName(c engineTypes.ContainerJSON, seed int) string {
	var serviceName string
	if c.Name != "" {
		if strings.HasPrefix(c.Name, "/") {
			serviceName = c.Name[1:]
		} else {
			serviceName = c.Name
		}
	} else {
		serviceName = fmt.Sprintf("service-%d", seed)
	}

	return serviceName
}

func getServiceBindingsPorts(c engineTypes.ContainerJSON) ([]types.ServicePortConfig, types.StringOrNumberList) {
	ports := []types.ServicePortConfig{}
	portsBinding := map[string]bool{}

	for port, bindings := range c.HostConfig.PortBindings {
		if len(bindings) > 0 {
			for _, binding := range bindings {
				portConfig := types.ServicePortConfig{}
				portConfig.HostIP = binding.HostIP
				portConfig.Published = binding.HostPort
				portConfig.Target = uint32(port.Int())
				ports = append(ports, portConfig)
				// Keep track of the ports that are bound
				// to compare to exposed ports later
				portsBinding[port.Port()] = true
			}
		}
	}

	exposedPorts := types.StringOrNumberList{}
	for exposed := range c.Config.ExposedPorts {
		// Make a list of the exposed ports, filtering out ports that are bound
		// to the host
		if _, ok := portsBinding[exposed.Port()]; !ok {
			exposedPorts = append(exposedPorts, exposed.Port())
		}

	}

	return ports, exposedPorts
}

func getServiceEnv(c engineTypes.ContainerJSON) types.MappingWithEquals {
	// TODO: Test environment file as input to see how that data gets populated;
	// there may not be anything additional I need to do here
	return types.NewMappingWithEquals(c.Config.Env)
}

func getServiceEntrypoint(c engineTypes.ContainerJSON) types.ShellCommand {
	// TODO: Entrypoint should only be specified when it differs from the image's entrypoint
	// TODO: How to tell the difference between an empty entrypoint and one that isn't specified?
	return types.ShellCommand(c.Config.Entrypoint)
}

func getServiceCmd(c engineTypes.ContainerJSON) types.ShellCommand {
	return types.ShellCommand(c.Config.Cmd)
}

func getServiceMounts(c engineTypes.ContainerJSON) []types.ServiceVolumeConfig {
	mountsLen := len(c.HostConfig.Binds) + len(c.HostConfig.Mounts)
	mounts := make([]types.ServiceVolumeConfig, 0, mountsLen)

	// binds array of strings in c.HostConfig.Binds
	for _, bString := range c.HostConfig.Binds {
		options := strings.Split(bString, ":")
		if len(options) < 2 {
			// TODO: handle this case properly
			fmt.Printf("unable to process bind mount: %s", bString)
			continue
		}

		// TODO: The source of this might be a volume and not a host folder
		// I'm not sure the canonical way to structure / reference these
		bind := types.ServiceVolumeConfig{
			Type:     "bind",
			Source:   options[0],
			Target:   options[1],
			ReadOnly: true,
		}
		// TODO: I'm not sure if this is the proper way to map this?
		if len(options) == 3 && options[2] == "rw" {
			bind.ReadOnly = false
		}

		// TODO: There are probably other things / defaults that need to be mapped properly,
		// following the .String() method on this return type!
		mounts = append(mounts, bind)
	}

	// list of mount structs in c.HostConfig.Mounts
	for _, m := range c.HostConfig.Mounts {
		mount := types.ServiceVolumeConfig{
			Type:        string(m.Type),
			Source:      m.Source,
			Target:      m.Target,
			ReadOnly:    m.ReadOnly,
			Consistency: string(m.Consistency),
		}
		if m.BindOptions != nil {
			mount.Bind = &types.ServiceVolumeBind{
				Propagation:    string(m.BindOptions.Propagation),
				CreateHostPath: m.BindOptions.CreateMountpoint,
				// selinux = > does this map?
				// extensions => does this map?
			}
		}
		if m.VolumeOptions != nil {
			mount.Volume = &types.ServiceVolumeVolume{
				NoCopy:  m.VolumeOptions.NoCopy,
				Subpath: m.VolumeOptions.Subpath,
				// extensions => does this map?
			}
		}
		if m.TmpfsOptions != nil {
			mount.Tmpfs = &types.ServiceVolumeTmpfs{
				Size: types.UnitBytes(m.TmpfsOptions.SizeBytes),
				Mode: uint32(m.TmpfsOptions.Mode),
				// extensions => does this map?
			}
		}
		mounts = append(mounts, mount)
	}

	// TODO: some of this data is going to be better served in a separate volumes declaration

	return mounts
}

func getServiceImage(c engineTypes.ContainerJSON) string {
	// hehe this is extremely hand-wavy and won't work in that many cases.
	// The Config.Image is the image reference that the container was run with
	return c.Config.Image
}
