import axios from 'axios'
import { ElMessage } from 'element-plus'

const api = axios.create({
  baseURL: '/api/v1',
  timeout: 10000
})

api.interceptors.response.use(
  response => response.data,
  error => {
    ElMessage.error(error.response?.data?.message || 'API request failed')
    return Promise.reject(error)
  }
)

export const getDashboard = () => api.get('/dashboard')

export const getRules = () => api.get('/rules')
export const createRule = (data) => api.post('/rules', data)
export const updateRule = (id, data) => api.put(`/rules/${id}`, data)
export const deleteRule = (id) => api.delete(`/rules/${id}`)

export const getLogs = (params) => api.get('/logs', { params })
export const confirmAction = (id, code) => api.post(`/actions/${id}/confirm`, { code })

export const getSettings = () => api.get('/settings')
export const updateSettings = (data) => api.put('/settings', data)

export const getUsers = () => api.get('/users')
