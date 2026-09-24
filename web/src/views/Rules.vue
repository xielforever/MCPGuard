<template>
  <div class="rules">
    <div class="toolbar">
      <el-input v-model="searchQuery" placeholder="Search rules..." style="width: 200px" prefix-icon="Search" />
      <el-button type="primary" @click="openDialog()">Add Rule</el-button>
    </div>

    <el-table :data="rules" style="width: 100%" class="mt-20">
      <el-table-column prop="name" label="Name" />
      <el-table-column prop="description" label="Description" />
      <el-table-column prop="toolName" label="Tool Name" />
      <el-table-column prop="pattern" label="Pattern" />
      <el-table-column prop="action" label="Action">
        <template #default="{ row }">
          <el-tag :type="getActionType(row.action)">{{ row.action }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="priority" label="Priority" width="100" />
      <el-table-column prop="enabled" label="Enabled" width="100">
        <template #default="{ row }">
          <el-switch v-model="row.enabled" />
        </template>
      </el-table-column>
      <el-table-column label="Actions" width="150">
        <template #default="{ row }">
          <el-button link type="primary" @click="openDialog(row)">Edit</el-button>
          <el-button link type="danger" @click="confirmDelete(row)">Delete</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog :title="isEdit ? 'Edit Rule' : 'Add Rule'" v-model="dialogVisible" width="500px">
      <el-form :model="form" label-width="100px">
        <el-form-item label="Name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="Description">
          <el-input v-model="form.description" />
        </el-form-item>
        <el-form-item label="Tool Name">
          <el-input v-model="form.toolName" placeholder="e.g. run_command, *" />
        </el-form-item>
        <el-form-item label="Pattern">
          <el-input v-model="form.pattern" placeholder="Regex pattern" />
        </el-form-item>
        <el-form-item label="Action">
          <el-select v-model="form.action" style="width: 100%">
            <el-option label="Pass" value="pass" />
            <el-option label="Confirm" value="confirm" />
            <el-option label="Block" value="block" />
          </el-select>
        </el-form-item>
        <el-form-item label="Priority">
          <el-input-number v-model="form.priority" :min="0" />
        </el-form-item>
        <el-form-item label="Enabled">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <span class="dialog-footer">
          <el-button @click="dialogVisible = false">Cancel</el-button>
          <el-button type="primary" @click="saveRule">Save</el-button>
        </span>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { ElMessageBox, ElMessage } from 'element-plus'

const searchQuery = ref('')
const dialogVisible = ref(false)
const isEdit = ref(false)

const form = ref({
  name: '', description: '', toolName: '', pattern: '', action: 'confirm', priority: 0, enabled: true
})

const rules = ref([
  { id: 1, name: 'Block rm -rf', description: 'Prevent dangerous delete', toolName: 'run_command', pattern: 'rm -rf .*', action: 'block', priority: 100, enabled: true },
  { id: 2, name: 'Confirm prod deploy', description: 'Confirm before deploying', toolName: 'run_command', pattern: 'kubectl apply .*prod.*', action: 'confirm', priority: 90, enabled: true },
])

const getActionType = (action) => {
  const map = { pass: 'success', confirm: 'warning', block: 'danger' }
  return map[action] || 'info'
}

const openDialog = (row) => {
  isEdit.value = !!row
  if (row) {
    form.value = { ...row }
  } else {
    form.value = { name: '', description: '', toolName: '', pattern: '', action: 'confirm', priority: 0, enabled: true }
  }
  dialogVisible.value = true
}

const saveRule = () => {
  // Mock save
  dialogVisible.value = false
  ElMessage.success('Saved successfully')
}

const confirmDelete = (row) => {
  ElMessageBox.confirm('Are you sure you want to delete this rule?', 'Warning', { type: 'warning' })
    .then(() => {
      ElMessage.success('Deleted successfully')
    })
    .catch(() => {})
}
</script>

<style scoped>
.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.mt-20 {
  margin-top: 20px;
}
</style>
