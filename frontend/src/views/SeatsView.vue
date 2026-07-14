<template>
  <div style="max-width:720px;margin:32px auto;padding:0 16px">
    <router-link to="/" style="color:#666;font-size:0.9em;text-decoration:none">&larr; Back to showtimes</router-link>
    <h2 style="color:#1a1a2e;margin-top:12px">Select a Seat</h2>

    <p v-if="loading" style="color:#888">Loading seats…</p>
    <p v-if="error"   style="color:#e94560">{{ error }}</p>

    <!-- Legend -->
    <div style="display:flex;gap:20px;margin-bottom:20px;flex-wrap:wrap;font-size:0.85em">
      <span v-for="l in legend" :key="l.label" style="display:flex;align-items:center;gap:6px">
        <span :style="`display:inline-block;width:14px;height:14px;border-radius:3px;background:${l.color}`"></span>
        {{ l.label }}
      </span>
    </div>

    <!-- Seat grid, grouped by row -->
    <div style="background:#fff;border-radius:8px;padding:20px;box-shadow:0 1px 4px rgba(0,0,0,.08)">
      <div
        v-for="(rowSeats, row) in seatsByRow" :key="row"
        style="display:flex;align-items:center;gap:6px;margin-bottom:8px"
      >
        <span style="width:22px;font-weight:700;color:#555;font-size:0.85em">{{ row }}</span>
        <button
          v-for="seat in rowSeats" :key="seat.id"
          @click="onSeatClick(seat)"
          :disabled="seat.status !== 'AVAILABLE' && selected?.id !== seat.id"
          :style="seatStyle(seat)"
          :title="`${row}${seat.number} — ${seat.status}`"
          style="width:38px;height:38px;border-radius:4px;border:none;font-size:0.8em;font-weight:600;transition:opacity .15s"
        >{{ seat.number }}</button>
      </div>
      <!-- Screen indicator -->
      <div style="margin-top:16px;text-align:center;color:#999;font-size:0.8em;border-top:3px solid #ddd;padding-top:8px">
        SCREEN
      </div>
    </div>

    <!-- Hold panel — shown after a successful select -->
    <div
      v-if="selected"
      style="margin-top:20px;padding:20px;background:#fff;border:2px solid #2196f3;border-radius:8px;box-shadow:0 1px 4px rgba(0,0,0,.08)"
    >
      <p style="margin-top:0;font-size:1em">
        Holding seat <strong>{{ selected.row }}{{ selected.number }}</strong>
        &nbsp;—&nbsp; expires in <strong style="color:#e94560">{{ countdownLabel }}</strong>
      </p>
      <button
        @click="pay"
        :disabled="paying || paid"
        style="padding:10px 28px;background:#4caf50;color:#fff;border:none;border-radius:4px;font-size:1rem"
      >{{ paid ? 'Booked!' : paying ? 'Processing…' : 'Pay & Confirm' }}</button>
      <button
        v-if="!paid"
        @click="cancelHold"
        style="margin-left:12px;padding:10px 16px;background:#ddd;border:none;border-radius:4px;font-size:0.9rem"
      >Cancel</button>
      <p v-if="payError" style="color:#e94560;margin-top:8px">{{ payError }}</p>
    </div>

    <!-- Real-time log -->
    <div v-if="events.length" style="margin-top:20px;font-size:0.8em;color:#666">
      <strong>Live events:</strong>
      <span v-for="(e, i) in events" :key="i" style="margin-left:8px;background:#f0f0f0;padding:2px 6px;border-radius:3px">
        {{ e }}
      </span>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import api from '../lib/api'
import { useAuthStore } from '../stores/auth'

const route       = useRoute()
const auth        = useAuthStore()
const showtimeId  = route.params.id

const seats    = ref([])
const loading  = ref(true)
const error    = ref('')
const selected = ref(null)   // { id, row, number, ownerToken }
const countdown = ref(0)
const paying   = ref(false)
const paid     = ref(false)
const payError = ref('')
const events   = ref([])     // last 5 realtime event labels

let ws             = null
let countdownTimer = null
let reconnectTimer = null
let reconnectDelay = 1000   // ms; doubles per failed attempt, capped at 15s
let closedByUs      = false // true once onUnmounted intentionally closes the socket

const legend = [
  { label: 'Available', color: '#4caf50' },
  { label: 'Locked',    color: '#ff9800' },
  { label: 'Booked',    color: '#f44336' },
  { label: 'Selected',  color: '#2196f3' },
]

const seatsByRow = computed(() => {
  const map = {}
  for (const s of seats.value) {
    ;(map[s.row] ??= []).push(s)
  }
  return map
})

const countdownLabel = computed(() => {
  const m = Math.floor(countdown.value / 60)
  const s = countdown.value % 60
  return `${m}:${String(s).padStart(2, '0')}`
})

function seatStyle(seat) {
  if (selected.value?.id === seat.id) return 'background:#2196f3;color:#fff;cursor:pointer'
  const bg = { BOOKED: '#f44336', LOCKED: '#ff9800', AVAILABLE: '#4caf50' }[seat.status] ?? '#ccc'
  const cursor = seat.status === 'AVAILABLE' ? 'pointer' : 'not-allowed'
  return `background:${bg};color:#fff;cursor:${cursor}`
}

async function loadSeats() {
  try {
    const { data } = await api.get(`/showtimes/${showtimeId}/seats`)
    seats.value = data.seats ?? []
  } catch {
    error.value = 'Failed to load seat map'
  } finally {
    loading.value = false
  }
}

function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  // The browser WebSocket API cannot set custom headers during the upgrade,
  // so the JWT travels as a WS subprotocol ("bearer, <jwt>") instead of a URL
  // query param — it never appears in the URL, so it can't leak into access
  // logs, browser history, or a Referer header.
  ws = new WebSocket(
    `${proto}//${location.host}/api/ws?showtimeId=${showtimeId}`,
    ['bearer', auth.token]
  )

  ws.onopen = () => {
    reconnectDelay = 1000 // connection succeeded — reset backoff for next time
  }

  ws.onmessage = (evt) => {
    try {
      const ev = JSON.parse(evt.data)
      applyEvent(ev)
      pushEventLabel(ev)
    } catch {}
  }

  ws.onclose = () => {
    // onUnmounted closes the socket on navigation away from this view; that
    // close must NOT trigger a reconnect, or the timer outlives the component.
    if (closedByUs) return
    // Refetch on reconnect to catch any events missed during the gap.
    // Backoff (1s, 2s, 4s, ... capped at 15s) avoids hammering the backend
    // if it's down for a while.
    reconnectTimer = setTimeout(() => {
      connectWS()
      loadSeats()
    }, reconnectDelay)
    reconnectDelay = Math.min(reconnectDelay * 2, 15000)
  }
}

function applyEvent(ev) {
  const statusMap = { SEAT_LOCKED: 'LOCKED', SEAT_BOOKED: 'BOOKED', SEAT_RELEASED: 'AVAILABLE' }
  const newStatus = statusMap[ev.type]
  if (!newStatus) return

  const seat = seats.value.find(s => s.id === ev.seatId)
  if (seat) seat.status = newStatus

  // If someone else booked or the backend released our held seat, clear the panel
  if (selected.value?.id === ev.seatId && !paid.value) {
    if (ev.type === 'SEAT_BOOKED' || ev.type === 'SEAT_RELEASED') clearHold()
  }
}

function pushEventLabel(ev) {
  events.value.unshift(`${ev.type} ${ev.seatId.slice(-4)}`)
  if (events.value.length > 5) events.value.pop()
}

async function onSeatClick(seat) {
  if (seat.status !== 'AVAILABLE') return
  clearHold()
  error.value = ''
  try {
    const { data } = await api.post(`/showtimes/${showtimeId}/seats/${seat.id}/select`)
    selected.value = { id: seat.id, row: seat.row, number: seat.number, ownerToken: data.ownerToken }
    seat.status = 'LOCKED'
    startCountdown(data.expiresAt)
  } catch (e) {
    const status = e.response?.status
    error.value = e.response?.data?.error || 'Could not hold seat'
    // 409 = already locked/booked — refetch so we show the correct status
    if (status === 409 || status === 410) loadSeats()
  }
}

function startCountdown(expiresAt) {
  clearInterval(countdownTimer)
  const exp = new Date(expiresAt).getTime()
  countdownTimer = setInterval(() => {
    const secs = Math.max(0, Math.round((exp - Date.now()) / 1000))
    countdown.value = secs
    if (secs === 0) {
      clearInterval(countdownTimer)
      if (!paid.value) {
        // Optimistically reset; SEAT_RELEASED WS event will also arrive shortly
        const s = seats.value.find(x => x.id === selected.value?.id)
        if (s) s.status = 'AVAILABLE'
        clearHold()
      }
    }
  }, 500)
}

async function pay() {
  if (!selected.value) return
  paying.value = true
  payError.value = ''
  try {
    await api.post(`/showtimes/${showtimeId}/seats/${selected.value.id}/pay`, {
      ownerToken: selected.value.ownerToken,
    })
    paid.value = true
    const s = seats.value.find(x => x.id === selected.value?.id)
    if (s) s.status = 'BOOKED'
    clearInterval(countdownTimer)
    setTimeout(clearHold, 1800)
  } catch (e) {
    const status = e.response?.status
    payError.value = e.response?.data?.error || 'Payment failed'
    paying.value = false
    // 409 = double-book caught; 410 = hold expired — clear panel and refetch
    if (status === 409 || status === 410) {
      clearHold()
      loadSeats()
    }
  }
}

async function cancelHold() {
  const seatId = selected.value?.id
  const ownerToken = selected.value?.ownerToken
  const s = seats.value.find(x => x.id === seatId)

  // Best-effort: ask the backend to release the lock immediately so the seat
  // is free for others right away instead of sitting LOCKED until the TTL
  // expires. If this fails the seat just falls back to the timeout path.
  if (seatId && ownerToken) {
    try {
      await api.delete(`/showtimes/${showtimeId}/seats/${seatId}/select`, {
        data: { ownerToken },
      })
    } catch {}
  }

  // Optimistically mark it available so the user isn't stuck seeing it as LOCKED.
  if (s && !paid.value) s.status = 'AVAILABLE'
  clearHold()
}

function clearHold() {
  selected.value = null
  countdown.value = 0
  paying.value = false
  paid.value = false
  payError.value = ''
  clearInterval(countdownTimer)
}

onMounted(() => {
  loadSeats()
  connectWS()
})

onUnmounted(() => {
  closedByUs = true
  clearTimeout(reconnectTimer)
  ws?.close()
  clearInterval(countdownTimer)
})
</script>
