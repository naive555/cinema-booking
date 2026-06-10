import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const routes = [
  { path: '/login',         component: () => import('../views/LoginView.vue') },
  { path: '/auth/callback', component: () => import('../views/AuthCallbackView.vue') },
  {
    path: '/',
    component: () => import('../views/ShowtimesView.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/showtimes/:id/seats',
    component: () => import('../views/SeatsView.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/admin',
    component: () => import('../views/AdminView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
]

const router = createRouter({ history: createWebHistory(), routes })

router.beforeEach((to) => {
  const auth = useAuthStore()
  if (to.meta.requiresAuth && !auth.isLoggedIn) return '/login'
  if (to.meta.requiresAdmin && !auth.isAdmin) return '/'
})

export default router
