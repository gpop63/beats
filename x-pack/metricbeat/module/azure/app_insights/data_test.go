// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package app_insights

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/preview/appinsights/v1/insights"
	"github.com/Azure/go-autorest/autorest/date"
	"github.com/stretchr/testify/assert"

	"github.com/elastic/elastic-agent-libs/mapstr"
)

func TestGroupByDimensions(t *testing.T) {
	start := &date.Time{Time: time.Now()}
	end := &date.Time{Time: time.Now()}

	v := []MetricValue{
		{
			SegmentName: map[string]string{},
			Value:       map[string]interface{}{},
			Segments: []MetricValue{
				{
					SegmentName: map[string]string{},
					Value:       map[string]interface{}{},
					Segments: []MetricValue{
						{
							SegmentName: map[string]string{
								"request_url_host": "",
							},
							Value: map[string]interface{}{
								"users_count.unique": 44,
							},
							Segments: nil,
							Interval: "",
							Start:    nil,
							End:      nil,
						},
					},
					Interval: "",
					Start:    nil,
					End:      nil,
				},
			},
			Interval: "P5M",
			Start:    start,
			End:      start,
		},
		{
			SegmentName: map[string]string{},
			Value:       map[string]interface{}{},
			Segments: []MetricValue{
				{
					SegmentName: map[string]string{},
					Value:       map[string]interface{}{},
					Segments: []MetricValue{
						{
							SegmentName: map[string]string{
								"request_url_host": "",
							},
							Value: map[string]interface{}{
								"sessions_count.unique": 44,
							},
							Segments: nil,
							Interval: "",
							Start:    nil,
							End:      nil,
						},
					},
					Interval: "",
					Start:    nil,
					End:      nil,
				},
			},
			Interval: "P5M",
			Start:    start,
			End:      end,
		},
		{
			SegmentName: map[string]string{},
			Value:       map[string]interface{}{},
			Segments: []MetricValue{
				{
					SegmentName: map[string]string{},
					Value:       map[string]interface{}{},
					Segments: []MetricValue{
						{
							SegmentName: map[string]string{
								"request_url_host": "localhost",
							},
							Value: map[string]interface{}{
								"sessions_count.unique": 44,
							},
							Segments: nil,
							Interval: "",
							Start:    nil,
							End:      nil,
						},
					},
					Interval: "",
					Start:    nil,
					End:      nil,
				},
			},
			Interval: "P5M",
			Start:    start,
			End:      end,
		},
	}

	dimensionsGrouped, _ := groupMetricsByDimension(v)
	assert.Len(t, dimensionsGrouped, 2)

	group1, ok := dimensionsGrouped["request_url_host"]
	assert.True(t, ok)
	assert.Len(t, group1, 2)

	group2, ok := dimensionsGrouped["request_url_hostlocalhost"]
	assert.True(t, ok)
	assert.Len(t, group2, 1)
}

func TestEventMapping(t *testing.T) {
	startDate := date.Time{}
	id := "123"
	var info = insights.MetricsResultInfo{
		AdditionalProperties: map[string]interface{}{
			"requests/count":  map[string]interface{}{"sum": 12},
			"requests/failed": map[string]interface{}{"sum": 10},
		},
		Start: &startDate,
		End:   &startDate,
	}
	var metricResult = insights.MetricsResult{
		Value: &info,
	}
	metrics := []insights.MetricsResultsItem{
		{
			ID:     &id,
			Status: nil,
			Body:   &metricResult,
		},
	}
	var result = insights.ListMetricsResultsItem{
		Value: &metrics,
	}
	applicationId := "abc"
	events := EventsMapping(result, applicationId, "")
	assert.Equal(t, len(events), 1)
	for _, event := range events {
		val1, _ := event.MetricSetFields.GetValue("start_date")
		assert.Equal(t, val1, &startDate)
		val2, _ := event.MetricSetFields.GetValue("end_date")
		assert.Equal(t, val2, &startDate)
		val3, _ := event.ModuleFields.GetValue("metrics.requests_count")
		assert.Equal(t, val3, mapstr.M{"sum": 12})
		val5, _ := event.ModuleFields.GetValue("metrics.requests_failed")
		assert.Equal(t, val5, mapstr.M{"sum": 10})
		val4, _ := event.ModuleFields.GetValue("application_id")
		assert.Equal(t, val4, applicationId)

	}

}

func TestCleanMetricNames(t *testing.T) {
	ex := "customDimensions/ExecutingAssemblyFileVersion"
	result := cleanMetricNames(ex)
	assert.Equal(t, result, "custom_dimensions_executing_assembly_file_version")
	ex = "customDimensions/_MS.AggregationIntervalMs"
	result = cleanMetricNames(ex)
	assert.Equal(t, result, "custom_dimensions__ms_aggregation_interval_ms")
	ex = "customDimensions/_MS.IsAutocollected"
	result = cleanMetricNames(ex)
	assert.Equal(t, result, "custom_dimensions__ms_is_autocollected")
}
