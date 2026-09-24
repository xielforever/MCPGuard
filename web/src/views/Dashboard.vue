<template>
  <div class="dashboard">
    <el-row :gutter="20" class="stat-cards">
      <el-col :span="6">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-icon blue"><el-icon><DataLine /></el-icon></div>
          <div class="stat-content">
            <div class="stat-title">Total Operations</div>
            <div class="stat-value">1,234</div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-icon orange"><el-icon><Warning /></el-icon></div>
          <div class="stat-content">
            <div class="stat-title">Intercepted</div>
            <div class="stat-value">56</div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-icon green"><el-icon><CircleCheck /></el-icon></div>
          <div class="stat-content">
            <div class="stat-title">Confirmed</div>
            <div class="stat-value">42</div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-icon red"><el-icon><CircleClose /></el-icon></div>
          <div class="stat-content">
            <div class="stat-title">Blocked</div>
            <div class="stat-value">14</div>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="20" class="mt-20">
      <el-col :span="24">
        <el-card shadow="never">
          <template #header>
            <div class="card-header">
              <span>Operations Over Time</span>
            </div>
          </template>
          <div class="chart-container">
            <v-chart class="chart" :option="chartOption" />
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="20" class="mt-20">
      <el-col :span="24">
        <el-card shadow="never">
          <template #header>
            <div class="card-header">
              <span>Recent Activity</span>
            </div>
          </template>
          <el-table :data="recentActivities" style="width: 100%">
            <el-table-column prop="time" label="Time" width="180" />
            <el-table-column prop="tool" label="Tool" />
            <el-table-column prop="action" label="Action" />
            <el-table-column prop="rule" label="Rule" />
            <el-table-column prop="status" label="Status">
              <template #default="{ row }">
                <el-tag :type="getStatusType(row.status)">{{ row.status }}</el-tag>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup>
import { ref, provide } from 'vue'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import VChart, { THEME_KEY } from 'vue-echarts'

use([CanvasRenderer, LineChart, GridComponent, TooltipComponent, LegendComponent])

const chartOption = ref({
  tooltip: { trigger: 'axis' },
  legend: { data: ['Passed', 'Intercepted'] },
  xAxis: {
    type: 'category',
    data: ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']
  },
  yAxis: { type: 'value' },
  series: [
    {
      name: 'Passed',
      type: 'line',
      data: [120, 132, 101, 134, 90, 230, 210],
      smooth: true
    },
    {
      name: 'Intercepted',
      type: 'line',
      data: [20, 12, 11, 24, 9, 30, 21],
      smooth: true
    }
  ]
})

const recentActivities = ref([
  { time: '2023-11-20 10:00:00', tool: 'git_commit', action: 'Commit code', rule: 'Require confirm', status: 'confirmed' },
  { time: '2023-11-20 09:55:00', tool: 'npm_publish', action: 'Publish package', rule: 'Block publish', status: 'blocked' },
  { time: '2023-11-20 09:50:00', tool: 'read_file', action: 'Read config', rule: 'Allow read', status: 'passed' },
])

const getStatusType = (status) => {
  const map = { passed: 'success', confirmed: 'success', intercepted: 'warning', blocked: 'danger' }
  return map[status] || 'info'
}
</script>

<style scoped>
.stat-card {
  display: flex;
  align-items: center;
}
.stat-card :deep(.el-card__body) {
  display: flex;
  align-items: center;
  width: 100%;
  padding: 20px;
}
.stat-icon {
  font-size: 48px;
  margin-right: 20px;
}
.stat-icon.blue { color: #409EFF; }
.stat-icon.orange { color: #E6A23C; }
.stat-icon.green { color: #67C23A; }
.stat-icon.red { color: #F56C6C; }
.stat-content {
  flex: 1;
}
.stat-title {
  color: #909399;
  font-size: 14px;
  margin-bottom: 8px;
}
.stat-value {
  font-size: 24px;
  font-weight: bold;
  color: #303133;
}
.mt-20 {
  margin-top: 20px;
}
.chart-container {
  height: 300px;
}
.chart {
  height: 100%;
  width: 100%;
}
</style>
