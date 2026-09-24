import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/',
    name: 'Dashboard',
    component: () => import('../views/Dashboard.vue')
  },
  {
    path: '/rules',
    name: 'Rules',
    component: () => import('../views/Rules.vue')
  },
  {
    path: '/logs',
    name: 'AuditLog',
    component: () => import('../views/AuditLog.vue')
  },
  {
    path: '/users',
    name: 'Users',
    component: () => import('../views/Users.vue')
  },
  {
    path: '/settings',
    name: 'Settings',
    component: () => import('../views/Settings.vue')
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

export default router
