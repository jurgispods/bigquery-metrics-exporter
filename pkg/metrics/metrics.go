package metrics

import (
	"context"
	"fmt"
	"github.com/ovotech/bigquery-metrics-extractor/pkg/config"
	"github.com/rs/zerolog/log"
	"sort"
	"strings"
	"sync"
	"time"
)

type Metric struct {
	Interval uint64      `json:"interval"`
	Metric   string      `json:"metric"`
	Points   [][]float64 `json:"points"`
	Tags     []string    `json:"tags"`
	Type     string      `json:"type"`
}

const TypeGauge = "gauge"

func (m *Metric) Id() string {
	sb := strings.Builder{}
	sb.WriteString(m.Metric)
	for _, tag := range m.Tags {
		sb.WriteRune(';')
		sb.WriteString(tag)
	}
	return sb.String()
}

type Reading struct {
	Timestamp time.Time
	Value     float64
}

// NewReading creates a new reading with the current timestamp
func NewReading(val float64) Reading {
	return Reading{
		Timestamp: time.Now(),
		Value:     val,
	}
}

func (r Reading) serialize() []float64 {
	return []float64{float64(r.Timestamp.Unix()), r.Value}
}

func (m *Metric) append(r Reading) {
	pm := createPointmap(m)

	point := r.serialize()
	if _, ok := pm[point[0]]; ok {
		*pm[point[0]] = point[1]
	} else {
		m.Points = append(m.Points, point)
	}
}

func (m *Metric) mergePoints(o *Metric) {
	pm := createPointmap(m)

	for _, point := range o.Points {
		if len(point) != 2 {
			continue
		}

		if _, ok := pm[point[0]]; ok {
			*pm[point[0]] = point[1]
		} else {
			m.Points = append(m.Points, point)
		}
	}
}

type Producer struct {
	config *config.Config
}

// NewProducer returns a metric Producer
func NewProducer(c *config.Config) Producer {
	return Producer{config: c}
}

// Produce adds a reading to a metric, creating the metric if necessary
func (p *Producer) Produce(metric string, read Reading, tags []string) *Metric {
	tags = append(tags, p.config.MetricTags...)
	sort.Strings(tags)

	return &Metric{
		Interval: uint64(p.config.MetricInterval.Seconds()),
		Metric:   getFullMetricName(p.config.MetricPrefix, metric),
		Points:   [][]float64{read.serialize()},
		Tags:     tags,
		Type:     TypeGauge,
	}
}

func getFullMetricName(prefix, metric string) string {
	sb := strings.Builder{}
	sb.WriteString(prefix)
	if prefix != "" && !strings.HasSuffix(prefix, ".") {
		sb.WriteRune('.')
	}
	if strings.HasPrefix(metric, ".") {
		metric = strings.TrimPrefix(metric, ".")
	}
	sb.WriteString(metric)
	return sb.String()
}

type pointmap map[float64]*float64

func createPointmap(m *Metric) pointmap {
	pm := make(pointmap)
	for _, point := range m.Points {
		if len(point) != 2 {
			continue
		}

		pm[point[0]] = &point[1]
	}
	return pm
}

type Consumer struct {
	mx      sync.Mutex
	metrics map[string]*Metric
}

func NewConsumer() *Consumer {
	var metrics map[string]*Metric
	metrics = make(map[string]*Metric)

	return &Consumer{metrics: metrics}
}

// Run will run the consumer, returning a channel to feed metrics into
func (c *Consumer) Run() chan *Metric {
	log.Debug().Msg("Starting metric consumer")

	metricRcvr := make(chan *Metric)
	go func() {
		for metric := range metricRcvr {
			c.consume(metric)
		}
	}()
	return metricRcvr
}

// Flush will return all currently consumed metrics and empty the metric buffer
func (c *Consumer) Flush() []Metric {
	c.mx.Lock()
	defer c.mx.Unlock()

	metrics := c.getMetrics()
	c.metrics = make(map[string]*Metric)
	return metrics
}

type publisher interface {
	PublishMetricsSet(context.Context, []Metric) error
}

// PublishTo will publish the metrics collected so far to the provided publisher
func (c *Consumer) PublishTo(ctx context.Context, pub publisher) error {
	c.mx.Lock()
	defer c.mx.Unlock()

	metrics := c.getMetrics()
	if len(metrics) == 0 {
		log.Debug().
			Int("metrics_count", 0).
			Msg("No metrics to publish")

		return nil
	}

	err := pub.PublishMetricsSet(ctx, metrics)
	if IsRecoverable(err) {
		return fmt.Errorf("error publishing %d metrics, %w", len(metrics), err)
	}

	c.metrics = make(map[string]*Metric)
	return err
}

func (c *Consumer) getMetrics() []Metric {
	var metrics []Metric
	metrics = make([]Metric, len(c.metrics))

	i := 0
	for _, metric := range c.metrics {
		metrics[i] = *metric
		i++
	}

	return metrics
}

func (c *Consumer) consume(m *Metric) {
	c.mx.Lock()
	defer c.mx.Unlock()

	if _, ok := c.metrics[m.Id()]; ok {
		c.metrics[m.Id()].mergePoints(m)
	} else {
		c.metrics[m.Id()] = m
	}
}