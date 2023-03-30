package goprofile

import (
	"context"
	"fmt"
	"github.com/alibaba/ilogtail/helper/profile"
	"github.com/alibaba/ilogtail/helper/profile/pyroscope/pprof"
	"github.com/alibaba/ilogtail/pkg/logger"
	"github.com/alibaba/ilogtail/pkg/pipeline"
	"github.com/mitchellh/mapstructure"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/pyroscope-io/pyroscope/pkg/ingestion"
	"github.com/pyroscope-io/pyroscope/pkg/scrape"
	"github.com/pyroscope-io/pyroscope/pkg/scrape/config"
	"github.com/pyroscope-io/pyroscope/pkg/scrape/discovery"
	"github.com/pyroscope-io/pyroscope/pkg/scrape/model"
	"github.com/pyroscope-io/pyroscope/pkg/storage/metadata"
	"github.com/pyroscope-io/pyroscope/pkg/util/bytesize"
	"github.com/sirupsen/logrus"
	"os"
	"time"
)

type Mode string

const (
	HostMode       Mode = "host"
	KubernetesMode Mode = "kubernetes"
)

type Register struct {
	// todo convert make an component to convert prometheus metrics to ilogtail statistics
}

func (r *Register) Register(collector prometheus.Collector) error {
	return nil
}
func (r *Register) MustRegister(collector ...prometheus.Collector) {
}
func (r *Register) Unregister(collector prometheus.Collector) bool {
	return true
}

type Ingestion struct {
	collector pipeline.Collector
}

func (i *Ingestion) Ingest(ctx context.Context, input *ingestion.IngestInput) error {
	logger.Debug(ctx, input.Format, input.Metadata.SpyName, "size", len(input.Profile.Profile))
	p := pprof.NewRawProfileByPull(input.Profile.Profile, input.Profile.PreviousProfile, input.Profile.SampleTypeConfig)
	getAggType := func() profile.AggType {
		switch input.Metadata.AggregationType {
		case metadata.AverageAggregationType:
			return profile.AvgAggType
		case metadata.SumAggregationType:
			return profile.SumAggType
		default:
			return profile.SumAggType
		}

	}
	logs, err := p.Parse(ctx, &profile.Meta{
		StartTime:       input.Metadata.StartTime,
		EndTime:         input.Metadata.EndTime,
		SpyName:         "go",
		SampleRate:      input.Metadata.SampleRate,
		Units:           profile.Units(input.Metadata.Units),
		AggregationType: getAggType(),
		Tags:            input.Metadata.Key.Labels(),
	}, map[string]string{})
	if err != nil {
		logger.Debug(context.Background(), "parse pprof fail err", err.Error())
		return err
	}
	for _, log := range logs {
		i.collector.AddRawLog(log)
	}
	return nil
}

type Manager struct {
	scrapeManager    *scrape.Manager
	discoveryManager *discovery.Manager
}

func NewManager(collector pipeline.Collector) *Manager {
	logrusLogger := logrus.New()
	logrusLogger.SetOutput(os.Stdout)
	if logger.DebugFlag() {
		logrusLogger.SetLevel(5)
	} else {
		logrusLogger.SetLevel(4)
	}
	m := new(Manager)
	m.discoveryManager = discovery.NewManager(logrusLogger)
	m.scrapeManager = scrape.NewManager(logrusLogger, &Ingestion{collector: collector}, prometheus.DefaultRegisterer, false)
	return m
}

func (m *Manager) Stop() {
	m.discoveryManager.Stop()
	m.scrapeManager.Stop()
}

func (m *Manager) Start(p *GoProfile) error {
	c := config.DefaultConfig()
	c.JobName = p.ctx.GetLogstore() + "_" + p.ctx.GetConfigName()
	c.ScrapeInterval = time.Second * time.Duration(p.Interval)
	c.ScrapeTimeout = time.Second * time.Duration(p.Timeout)
	c.BodySizeLimit = bytesize.KB * bytesize.ByteSize(p.BodyLimitSize)
	c.EnabledProfiles = p.EnabledProfiles
	labelSet := make(model.LabelSet)
	for key, val := range p.Labels {
		labelSet[model.LabelName(key)] = model.LabelValue(val)
	}
	switch p.Mode {
	case HostMode:
		var cfg StaticConfig
		if err := mapstructure.Decode(p.Config, &cfg); err != nil {
			return err
		}
		cfg.labelSet = labelSet
		c.ServiceDiscoveryConfigs = append(c.ServiceDiscoveryConfigs, &cfg)
	case KubernetesMode:
		var cfg KubernetesConfig
		if err := mapstructure.Decode(p.Config, &cfg); err != nil {
			return err
		}
		cfg.labelSet = labelSet
		c.ServiceDiscoveryConfigs = append(c.ServiceDiscoveryConfigs, &cfg)
	default:
		return fmt.Errorf("unsupported mode %s", p.Mode)
	}
	if err := m.scrapeManager.ApplyConfig([]*config.Config{c}); err != nil {
		return err
	}
	go func() {
		logger.Debug(context.Background(), "init discovery manager")
		if err := m.discoveryManager.ApplyConfig(map[string]discovery.Configs{
			c.JobName: c.ServiceDiscoveryConfigs,
		}); err != nil {
			logger.Error(context.Background(), "GOPROFILE_ALARM", "apply discovery config error", err.Error())
		}
		err := m.discoveryManager.Run()
		logger.Debug(context.Background(), "finish discovery manager")
		if err != nil {
			logger.Error(context.Background(), "GOPROFILE_ALARM", "discovery err", err.Error())
		}
	}()
	go func() {
		logger.Debug(context.Background(), "init scrape manager")
		err := m.scrapeManager.Run(m.discoveryManager.SyncCh())
		logger.Debug(context.Background(), "finish scrape manager")
		if err != nil {
			logger.Error(context.Background(), "GOPROFILE_ALARM", "scrape err", err.Error())
		}
	}()
	return nil
}