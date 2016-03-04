package router

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	ktypes "k8s.io/kubernetes/pkg/types"

	"github.com/openshift/origin/pkg/cmd/util"
	"github.com/openshift/origin/pkg/cmd/util/clientcmd"
	"github.com/openshift/origin/pkg/router/controller"
	templateplugin "github.com/openshift/origin/pkg/router/template"
	"github.com/openshift/origin/pkg/util/proc"
	"github.com/openshift/origin/pkg/version"
)

const (
	routerLong = `
Start a router

This command launches a router connected to your cluster master. The router listens for routes and endpoints
created by users and keeps a local router configuration up to date with those changes.

You may customize the router by providing your own --template and --reload scripts.

You may restrict the set of routes exposed to a single project (with --namespace), projects your client has
access to with a set of labels (--project-labels), namespaces matching a label (--namespace-labels), or all
namespaces (no argument). You can limit the routes to those matching a --labels or --fields selector. Note
that you must have a cluster-wide administrative role to view all namespaces.`
)

type TemplateRouterOptions struct {
	Config *clientcmd.Config

	TemplateRouter
	RouterStats
	RouterSelection
}

type TemplateRouter struct {
	RouterName             string
	WorkingDir             string
	TemplateFile           string
	ReloadScript           string
	ReloadInterval         time.Duration
	DefaultCertificate     string
	DefaultCertificatePath string
	RouterService          *ktypes.NamespacedName
}

func (o *TemplateRouter) Bind(flag *pflag.FlagSet) {
	flag.StringVar(&o.RouterName, "name", util.Env("ROUTER_SERVICE_NAME", "public"), "The name the router will identify itself with in the route status")
	flag.StringVar(&o.WorkingDir, "working-dir", "/var/lib/containers/router", "The working directory for the router plugin")
	flag.StringVar(&o.DefaultCertificate, "default-certificate", util.Env("DEFAULT_CERTIFICATE", ""), "The contents of a default certificate to use for routes that don't expose a TLS server cert; in PEM format")
	flag.StringVar(&o.DefaultCertificatePath, "default-certificate-path", util.Env("DEFAULT_CERTIFICATE_PATH", ""), "A path to default certificate to use for routes that don't expose a TLS server cert; in PEM format")
	flag.StringVar(&o.TemplateFile, "template", util.Env("TEMPLATE_FILE", ""), "The path to the template file to use")
	flag.StringVar(&o.ReloadScript, "reload", util.Env("RELOAD_SCRIPT", ""), "The path to the reload script to use")

	interval := util.Env("RELOAD_INTERVAL", "0s")

	var err error
	o.ReloadInterval, err = time.ParseDuration(interval)
	if err != nil {
		glog.Warningf("Invalid RELOAD_INTERVAL %q, ignoring ...", interval)
		o.ReloadInterval = time.Duration(0 * time.Second)
	}
	flag.DurationVar(&o.ReloadInterval, "interval", o.ReloadInterval, "Controls how often router reloads are invoked. Mutiple router reload requests are coalesced for the duration of this interval since the last reload time.")
}

type RouterStats struct {
	StatsPortString string
	StatsPassword   string
	StatsUsername   string

	StatsPort int
}

func (o *RouterStats) Bind(flag *pflag.FlagSet) {
	flag.StringVar(&o.StatsPortString, "stats-port", util.Env("STATS_PORT", ""), "If the underlying router implementation can provide statistics this is a hint to expose it on this port.")
	flag.StringVar(&o.StatsPassword, "stats-password", util.Env("STATS_PASSWORD", ""), "If the underlying router implementation can provide statistics this is the requested password for auth.")
	flag.StringVar(&o.StatsUsername, "stats-user", util.Env("STATS_USERNAME", ""), "If the underlying router implementation can provide statistics this is the requested username for auth.")
}

// NewCommndTemplateRouter provides CLI handler for the template router backend
func NewCommandTemplateRouter(name string) *cobra.Command {
	options := &TemplateRouterOptions{
		Config: clientcmd.NewConfig(),
	}
	options.Config.FromFile = true

	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s%s", name, clientcmd.ConfigSyntax),
		Short: "Start a router",
		Long:  routerLong,
		Run: func(c *cobra.Command, args []string) {
			options.RouterSelection.Namespace = cmdutil.GetFlagString(c, "namespace")
			cmdutil.CheckErr(options.Complete())
			cmdutil.CheckErr(options.Validate())
			cmdutil.CheckErr(options.Run())
		},
	}

	cmd.AddCommand(version.NewVersionCommand(name, false))

	flag := cmd.Flags()
	options.Config.Bind(flag)
	options.TemplateRouter.Bind(flag)
	options.RouterStats.Bind(flag)
	options.RouterSelection.Bind(flag)

	return cmd
}

func (o *TemplateRouterOptions) Complete() error {
	routerSvcName := util.Env("ROUTER_SERVICE_NAME", "")
	routerSvcNamespace := util.Env("ROUTER_SERVICE_NAMESPACE", "")
	if len(routerSvcName) > 0 {
		if len(routerSvcNamespace) == 0 {
			return fmt.Errorf("ROUTER_SERVICE_NAMESPACE is required when ROUTER_SERVICE_NAME is specified")
		}
		o.RouterService = &ktypes.NamespacedName{
			Namespace: routerSvcNamespace,
			Name:      routerSvcName,
		}
	}

	if len(o.StatsPortString) > 0 {
		statsPort, err := strconv.Atoi(o.StatsPortString)
		if err != nil {
			return fmt.Errorf("stat port is not valid: %v", err)
		}
		o.StatsPort = statsPort
	}

	if nsecs := int(o.ReloadInterval.Seconds()); nsecs < 0 {
		return fmt.Errorf("invalid reload interval: %v - must be a positive duration", nsecs)
	}

	return o.RouterSelection.Complete()
}

func (o *TemplateRouterOptions) Validate() error {
	if len(o.RouterName) == 0 {
		return errors.New("router must have a name to identify itself in route status")
	}
	if len(o.TemplateFile) == 0 {
		return errors.New("template file must be specified")
	}

	if len(o.ReloadScript) == 0 {
		return errors.New("reload script must be specified")
	}
	return nil
}

// Run launches a template router using the provided options. It never exits.
func (o *TemplateRouterOptions) Run() error {
	pluginCfg := templateplugin.TemplatePluginConfig{
		WorkingDir:             o.WorkingDir,
		TemplatePath:           o.TemplateFile,
		ReloadScriptPath:       o.ReloadScript,
		ReloadInterval:         o.ReloadInterval,
		DefaultCertificate:     o.DefaultCertificate,
		DefaultCertificatePath: o.DefaultCertificatePath,
		StatsPort:              o.StatsPort,
		StatsUsername:          o.StatsUsername,
		StatsPassword:          o.StatsPassword,
		PeerService:            o.RouterService,
		IncludeUDP:             o.RouterSelection.IncludeUDP,
	}

	templatePlugin, err := templateplugin.NewTemplatePlugin(pluginCfg)
	if err != nil {
		return err
	}

	oc, kc, err := o.Config.Clients()
	if err != nil {
		return err
	}

	statusPlugin := controller.NewStatusAdmitter(templatePlugin, oc, o.RouterName)
	plugin := controller.NewUniqueHost(statusPlugin, o.RouteSelectionFunc(), statusPlugin)

	factory := o.RouterSelection.NewFactory(oc, kc)
	controller := factory.Create(plugin)
	controller.Run()

	proc.StartReaper()

	select {}
}
