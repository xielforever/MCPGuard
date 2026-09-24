<template>
  <div class="audit-log">
    <div class="filter-bar">
      <el-date-picker
        v-model="dateRange"
        type="daterange"
        range-separator="To"
        start-placeholder="Start date"
        end-placeholder="End date"
        style="width: 300px; margin-right: 15px;"
      />
      <el-input v-model="toolName" placeholder="Tool Name" style="width: 200px; margin-right: 15px;" />
      <el-select v-model="status" placeholder="Status" style="width: 150px; margin-right: 15px;">
        <el-option label="All" value="" />
        <el-option label="Passed" value="passed" />
        <el-option label="Intercepted" value="intercepted" />
        <el-option label="Confirmed" value="confirmed" />
        <el-option label="Blocked" value="blocked" />
      </el-select>
      <el-button type="primary" icon="Search">Search</el-button>
      <el-button type="default" icon="Download" style="margin-left: auto;">Export CSV</el-button>
    </div>

    <el-table :data="logs" style="width: 100%" class="mt-20">
      <el-table-column prop="timestamp" label="Timestamp" width="180" />
      <el-table-column prop="toolName" label="Tool Name" width="150" />
      <el-table-column prop="parameters" label="Parameters">
        <template #default="{ row }">
          <el-popover placement="top-start" title="Parameters" :width="300" trigger="hover" :content="row.parameters">
            <template #reference>
              <span class="truncate">{{ row.parameters }}</span>
            </template>
          </el-popover>
        </template>
      </el-table-column>
      <el-table-column prop="ruleMatched" label="Rule Matched" width="150" />
      <el-table-column prop="confirmCode" label="Confirm Code" width="120" />
      <el-table-column prop="status" label="Status" width="100">
        <template #default="{ row }">
          <el-tag :type="getStatusType(row.status)">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="user" label="User" width="120" />
    </el-table>

    <div class="pagination-container">
      <el-pagination
        v-model:current-page="currentPage"
        v-model:page-size="pageSize"
        :page-sizes="[10, 20, 50, 100]"
        layout="total, sizes, prev, pager, next, jumper"
        :total="100"
      />
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'

const dateRange = ref([])
const toolName = ref('')
const status = ref('')
const currentPage = ref(1)
const pageSize = ref(20)

const logs = ref([
  { timestamp: '2023-11-20 10:00:00', toolName: 'run_command', parameters: 'kubectl apply -f deployment.yaml', action: 'deploy', ruleMatched: 'Confirm prod deploy', confirmCode: '123456', status: 'confirmed', user: 'admin' },
  { timestamp: '2023-11-20 09:55:00', toolName: 'run_command', parameters: 'rm -rf /usr/bin', action: 'delete', ruleMatched: 'Block rm -rf', confirmCode: '-', status: 'blocked', user: 'admin' },
])

const getStatusType = (status) => {
  const map = { passed: 'success', confirmed: 'success', intercepted: 'warning', blocked: 'danger' }
  return map[status] || 'info'
}
</script>

<style scoped>
.filter-bar {
  display: flex;
  align-items: center;
}
.mt-20 {
  margin-top: 20px;
}
.truncate {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  display: inline-block;
  max-width: 100%;
}
.pagination-container {
  margin-top: 20px;
  display: flex;
  justify-content: flex-end;
}
</style>
