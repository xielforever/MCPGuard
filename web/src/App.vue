<template>
  <el-container class="app-container">
    <el-aside width="200px" class="app-aside">
      <div class="logo">
        <h2>MCPGuard</h2>
      </div>
      <el-menu
        :default-active="activeMenu"
        class="el-menu-vertical"
        background-color="#304156"
        text-color="#bfcbd9"
        active-text-color="#409EFF"
        router
      >
        <el-menu-item index="/">
          <el-icon><DataBoard /></el-icon>
          <span>Dashboard</span>
        </el-menu-item>
        <el-menu-item index="/rules">
          <el-icon><Lock /></el-icon>
          <span>Rules</span>
        </el-menu-item>
        <el-menu-item index="/logs">
          <el-icon><Document /></el-icon>
          <span>Audit Log</span>
        </el-menu-item>
        <el-menu-item index="/users">
          <el-icon><User /></el-icon>
          <span>Users</span>
        </el-menu-item>
        <el-menu-item index="/settings">
          <el-icon><Setting /></el-icon>
          <span>Settings</span>
        </el-menu-item>
      </el-menu>
    </el-aside>
    <el-container>
      <el-header class="app-header">
        <div class="header-content">
          <span>Safety Authorization Gateway</span>
          <el-dropdown>
            <span class="el-dropdown-link">
              Admin<el-icon class="el-icon--right"><arrow-down /></el-icon>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item>Profile</el-dropdown-item>
                <el-dropdown-item divided>Logout</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-header>
      <el-main class="app-main">
        <router-view></router-view>
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup>
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()
const activeMenu = computed(() => route.path)
</script>

<style scoped>
.app-container {
  height: 100vh;
}
.app-aside {
  background-color: #304156;
}
.logo {
  height: 60px;
  line-height: 60px;
  text-align: center;
  color: #fff;
  border-bottom: 1px solid #1f2d3d;
}
.logo h2 {
  margin: 0;
  font-size: 20px;
}
.el-menu-vertical {
  border-right: none;
}
.app-header {
  background-color: #fff;
  border-bottom: 1px solid #e6e6e6;
  line-height: 60px;
}
.header-content {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.el-dropdown-link {
  cursor: pointer;
  color: #409EFF;
  display: flex;
  align-items: center;
}
.app-main {
  background-color: #f0f2f5;
  padding: 20px;
}
</style>
