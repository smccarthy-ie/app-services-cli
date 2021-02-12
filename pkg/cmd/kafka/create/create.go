package create

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/cmd/flag"
	flagutil "github.com/bf2fc6cc711aee1a0c2a/cli/pkg/cmdutil/flags"
	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/kafka"

	flagmsg "github.com/bf2fc6cc711aee1a0c2a/cli/internal/localizer/msg/common/flag"
	kafkamsg "github.com/bf2fc6cc711aee1a0c2a/cli/internal/localizer/msg/kafka"

	"github.com/bf2fc6cc711aee1a0c2a/cli/internal/localizer"

	kasclient "github.com/bf2fc6cc711aee1a0c2a/cli/pkg/api/kas/client"

	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/cloudprovider/cloudproviderutil"
	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/cloudregion/cloudregionutil"

	"github.com/AlecAivazis/survey/v2"
	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/dump"
	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/iostreams"

	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/connection"
	pkgKafka "github.com/bf2fc6cc711aee1a0c2a/cli/pkg/kafka"
	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/logging"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"

	"github.com/bf2fc6cc711aee1a0c2a/cli/internal/config"
	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/cmd/factory"
	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/cmd/kafka/flags"
	"github.com/bf2fc6cc711aee1a0c2a/cli/pkg/cmdutil"
)

type Options struct {
	name     string
	provider string
	region   string
	multiAZ  bool

	outputFormat string

	interactive bool

	IO         *iostreams.IOStreams
	Config     config.IConfig
	Connection func() (connection.Connection, error)
	Logger     func() (logging.Logger, error)
}

const (
	// default Kafka instance values
	defaultMultiAZ  = true
	defaultRegion   = "us-east-1"
	defaultProvider = "aws"
)

// NewCreateCommand creates a new command for creating kafkas.
func NewCreateCommand(f *factory.Factory) *cobra.Command {
	opts := &Options{
		IO:         f.IOStreams,
		Config:     f.Config,
		Connection: f.Connection,
		Logger:     f.Logger,

		multiAZ: defaultMultiAZ,
	}

	localizer.LoadMessageFiles("cmd/kafka", "cmd/kafka/create", "cmd/common/flags")

	cmd := &cobra.Command{
		Use:     localizer.MustLocalizeFromID("kafka.create.cmd.use"),
		Short:   localizer.MustLocalizeFromID("kafka.create.cmd.shortDescription"),
		Long:    localizer.MustLocalizeFromID("kafka.create.cmd.longDescription"),
		Example: localizer.MustLocalizeFromID("kafka.create.cmd.example"),
		Args:    cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.name = args[0]
			}

			if err := kafka.ValidateName(opts.name); err != nil {
				return err
			}

			if !opts.IO.CanPrompt() && opts.name == "" {
				return errors.New(localizer.MustLocalize(&localizer.Config{
					MessageID: flagmsg.RequiredNonInteractiveError,
					TemplateData: map[string]interface{}{
						"Flag": "name",
					},
				}))

			} else if opts.name == "" && opts.provider == "" && opts.region == "" {
				opts.interactive = true
			}

			validOutputFormats := flagutil.ValidOutputFormats
			if opts.outputFormat != "" && !flagutil.IsValidInput(opts.outputFormat, validOutputFormats...) {
				return flag.InvalidValueError("output", opts.outputFormat, validOutputFormats...)
			}

			return runCreate(opts)
		},
	}

	cmd.Flags().StringVar(&opts.provider, flags.FlagProvider, "", localizer.MustLocalizeFromID("kafka.create.flag.cloudProvider.description"))
	cmd.Flags().StringVar(&opts.region, flags.FlagRegion, "", localizer.MustLocalizeFromID("kafka.create.flag.cloudRegion.description"))
	cmd.Flags().StringVarP(&opts.outputFormat, "output", "o", "json", localizer.MustLocalizeFromID("kafka.common.flag.output.description"))

	return cmd
}

func runCreate(opts *Options) error {
	logger, err := opts.Logger()
	if err != nil {
		return err
	}

	cfg, err := opts.Config.Load()
	if err != nil {
		return err
	}

	connection, err := opts.Connection()
	if err != nil {
		return err
	}

	api := connection.API()

	var payload *kasclient.KafkaRequestPayload
	if opts.interactive {
		logger.Debug()

		payload, err = promptKafkaPayload(opts)
		if err != nil {
			return err
		}

	} else {
		if opts.provider == "" {
			opts.provider = defaultProvider
		}
		if opts.region == "" {
			opts.region = defaultRegion
		}

		payload = &kasclient.KafkaRequestPayload{
			Name:          opts.name,
			Region:        &opts.region,
			CloudProvider: &opts.provider,
			MultiAz:       &opts.multiAZ,
		}
	}

	logger.Debug(localizer.MustLocalize(&localizer.Config{
		MessageID: "kafka.create.log.debug.creatingKafka",
		TemplateData: map[string]interface{}{
			"Name": opts.name,
		},
	}))

	a := api.Kafka().CreateKafka(context.Background())
	a = a.KafkaRequestPayload(*payload)
	a = a.Async(true)
	response, _, apiErr := a.Execute()

	if apiErr.Error() != "" {
		return apiErr
	}

	logger.Info(localizer.MustLocalize(&localizer.Config{
		MessageID: "kafka.create.info.successMessage",
		TemplateData: map[string]interface{}{
			"Name": response.GetName(),
		},
	}))

	switch opts.outputFormat {
	case "json":
		data, _ := json.MarshalIndent(response, "", cmdutil.DefaultJSONIndent)
		_ = dump.JSON(opts.IO.Out, data)
	case "yaml", "yml":
		data, _ := yaml.Marshal(response)
		_ = dump.YAML(opts.IO.Out, data)
	}

	kafkaCfg := &config.KafkaConfig{
		ClusterID: *response.Id,
	}

	cfg.Services.Kafka = kafkaCfg
	if err := opts.Config.Save(cfg); err != nil {
		return fmt.Errorf("%v: %w", localizer.MustLocalizeFromID(kafkamsg.CouldNotUseKafkaError), err)
	}

	return nil
}

// Show a prompt to allow the user to interactively insert the data for their Kafka
func promptKafkaPayload(opts *Options) (payload *kasclient.KafkaRequestPayload, err error) {
	connection, err := opts.Connection()
	if err != nil {
		return nil, err
	}

	api := connection.API()

	// set type to store the answers from the prompt with defaults
	answers := struct {
		Name          string
		Region        string
		MultiAZ       bool
		CloudProvider string
	}{
		MultiAZ: defaultMultiAZ,
	}

	promptName := &survey.Input{
		Message: localizer.MustLocalizeFromID("kafka.create.input.name.message"),
		Help:    localizer.MustLocalizeFromID("kafka.create.input.name.help"),
	}

	err = survey.AskOne(promptName, &answers.Name, survey.WithValidator(pkgKafka.ValidateName))
	if err != nil {
		return nil, err
	}

	// fetch all cloud available providers
	cloudProviderResponse, _, apiErr := api.Kafka().ListCloudProviders(context.Background()).Execute()
	if apiErr.Error() != "" {
		return nil, apiErr
	}

	cloudProviders := cloudProviderResponse.GetItems()
	cloudProviderNames := cloudproviderutil.GetEnabledNames(cloudProviders)

	cloudProviderPrompt := &survey.Select{
		Message: localizer.MustLocalizeFromID("kafka.create.input.cloudProvider.message"),
		Options: cloudProviderNames,
	}

	err = survey.AskOne(cloudProviderPrompt, &answers.CloudProvider)
	if err != nil {
		return nil, err
	}

	// get the selected provider type from the name selected
	selectedCloudProvider := cloudproviderutil.FindByName(cloudProviders, answers.CloudProvider)

	// nolint
	cloudRegionResponse, _, apiErr := api.Kafka().ListCloudProviderRegions(context.Background(), selectedCloudProvider.GetId()).Execute()
	if apiErr.Error() != "" {
		return nil, apiErr
	}

	regions := cloudRegionResponse.GetItems()
	regionIDs := cloudregionutil.GetEnabledIDs(regions)

	regionPrompt := &survey.Select{
		Message: localizer.MustLocalizeFromID("kafka.create.input.cloudRegion.message"),
		Options: regionIDs,
		Help:    localizer.MustLocalizeFromID("kafka.create.input.cloudRegion.help"),
	}

	err = survey.AskOne(regionPrompt, &answers.Region)
	if err != nil {
		return nil, err
	}

	payload = &kasclient.KafkaRequestPayload{
		Name:          answers.Name,
		Region:        &answers.Region,
		CloudProvider: &answers.CloudProvider,
		MultiAz:       &answers.MultiAZ,
	}

	return payload, nil
}
