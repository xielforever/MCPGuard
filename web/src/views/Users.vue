<template>
  <div class="users">
    <div class="toolbar">
      <el-button type="primary" @click="openDialog()">Add User</el-button>
    </div>

    <el-table :data="users" style="width: 100%" class="mt-20">
      <el-table-column prop="username" label="Username" />
      <el-table-column prop="email" label="Email" />
      <el-table-column prop="imChannel" label="IM Channel" />
      <el-table-column prop="imId" label="IM ID" />
      <el-table-column prop="role" label="Role">
        <template #default="{ row }">
          <el-tag>{{ row.role }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="Actions" width="200">
        <template #default="{ row }">
          <el-button link type="primary" @click="openDialog(row)">Edit</el-button>
          <el-button link type="primary" @click="bindIM(row)">Bind IM</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog :title="isEdit ? 'Edit User' : 'Add User'" v-model="dialogVisible" width="500px">
      <el-form :model="form" label-width="100px">
        <el-form-item label="Username">
          <el-input v-model="form.username" />
        </el-form-item>
        <el-form-item label="Email">
          <el-input v-model="form.email" />
        </el-form-item>
        <el-form-item label="Role">
          <el-select v-model="form.role" style="width: 100%">
            <el-option label="Admin" value="admin" />
            <el-option label="User" value="user" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <span class="dialog-footer">
          <el-button @click="dialogVisible = false">Cancel</el-button>
          <el-button type="primary" @click="saveUser">Save</el-button>
        </span>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref } from 'vue'

const dialogVisible = ref(false)
const isEdit = ref(false)

const form = ref({ username: '', email: '', role: 'user' })

const users = ref([
  { id: 1, username: 'admin', email: 'admin@example.com', imChannel: 'telegram', imId: '123456789', role: 'admin' },
  { id: 2, username: 'user1', email: 'user1@example.com', imChannel: 'slack', imId: 'U123456', role: 'user' },
])

const openDialog = (row) => {
  isEdit.value = !!row
  if (row) {
    form.value = { ...row }
  } else {
    form.value = { username: '', email: '', role: 'user' }
  }
  dialogVisible.value = true
}

const saveUser = () => {
  dialogVisible.value = false
}

const bindIM = (row) => {
  // Placeholder for binding logic
}
</script>

<style scoped>
.toolbar {
  display: flex;
  justify-content: flex-end;
}
.mt-20 {
  margin-top: 20px;
}
</style>
