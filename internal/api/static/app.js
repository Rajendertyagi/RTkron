// State
let jobs = [];
let isConnected = false;
let expandedSchedules = new Set(); // Track which job schedules are expanded

// API Base URL - use relative path so browser automatically resolves prefix
function getApiBase() {
    return './api';
}

// initialize on page load
document.addEventListener('DOMContentLoaded', () => {
    loadConfig();
    loadJobs();
    loadPolicy();
    setInterval(loadJobs, 3000);

    // error banner close (data-action delegation)
    document.getElementById('error-banner').addEventListener('click', (e) => {
        if (e.target.closest('button[data-action="hide-error"]')) {
            hideError();
        }
    });

    // job card actions (run / delete / toggle schedule) via data-action delegation
    document.getElementById('jobs-container').addEventListener('click', (e) => {
        const btn = e.target.closest('button[data-action]');
        if (!btn) {
            return;
        }
        const action = btn.getAttribute('data-action');
        const id = btn.getAttribute('data-id');
        if (action === 'run-job') {
            handleRunJob(id);
        } else if (action === 'delete-job') {
            const name = btn.getAttribute('data-name') || id;
            handleDeleteJob(id, name);
        } else if (action === 'toggle-schedule') {
            toggleSchedule(id);
        }
    });

    // Codeg Tools
    document.getElementById('payload-builder-form').addEventListener('submit', submitPayloadBuilder);
    document.getElementById('policy-form').addEventListener('submit', submitPolicy);
    document.getElementById('policy-items').addEventListener('click', (e) => {
        const btn = e.target.closest('button[data-conn]');
        if (btn) {
            removePolicy(btn.getAttribute('data-conn'));
        }
    });
});

// load server configuration
async function loadConfig() {
    try {
        const response = await fetch(`${getApiBase()}/config`);
        if (response.ok) {
            const config = await response.json();
            if (config.title) {
                document.title = config.title;
                const mainTitle = document.getElementById('main-title');
                if (mainTitle) {
                    mainTitle.textContent = config.title;
                }
            }
        }
    } catch (err) {
        console.error('Failed to load config:', err);
    }
}

// poll the scheduler job list (no websocket needed for a single embedded app)
async function loadJobs() {
    try {
        const response = await fetch(`${getApiBase()}/jobs`, { cache: 'no-store' });
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }
        jobs = await response.json();
        updateConnectionStatus(true);
        hideError();
        renderJobs();
    } catch (err) {
        console.error('Failed to load jobs:', err);
        updateConnectionStatus(false);
    }
}

function updateConnectionStatus(connected) {
    const statusEl = document.getElementById('connection-status');
    if (connected) {
        statusEl.className = 'status-indicator connected';
        statusEl.textContent = '● Connected';
    } else {
        statusEl.className = 'status-indicator disconnected';
        statusEl.textContent = '○ Disconnected';
    }
}

// error handling
function showError(message) {
    const banner = document.getElementById('error-banner');
    const messageEl = document.getElementById('error-message');
    messageEl.textContent = message;
    banner.classList.remove('hidden');
}

function hideError() {
    document.getElementById('error-banner').classList.add('hidden');
}

// API Functions
async function deleteJob(id) {
    const response = await fetch(`${getApiBase()}/jobs/${id}`, {
        method: 'DELETE',
    });

    if (!response.ok) {
        throw new Error('Failed to delete job');
    }
}

async function runJob(id) {
    const response = await fetch(`${getApiBase()}/jobs/${id}/run`, {
        method: 'POST',
    });

    if (!response.ok) {
        throw new Error('Failed to run job');
    }
}

// Create a scheduled job via the Payload Builder form.
async function createScheduledJob(cronExpr, jobId, payload) {
    const body = { cron_expr: cronExpr, job_id: jobId };
    if (payload) {
        body.payload = payload;
    }
    const response = await fetch(`${getApiBase()}/jobs`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });

    if (!response.ok) {
        throw new Error('Failed to schedule job');
    }
}

// Add/remove an auto-approve policy rule for a connection_id.
async function updatePolicy(connectionId, action) {
    const response = await fetch(`${getApiBase()}/policy`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ connection_id: connectionId, action }),
    });

    if (!response.ok) {
        throw new Error('Failed to update policy');
    }
}

// job actions
async function handleRunJob(id) {
    try {
        await runJob(id);
        hideError();
        await loadJobs();
    } catch (err) {
        showError(err.message);
    }
}

async function handleDeleteJob(id, name) {
    if (!confirm(`Are you sure you want to delete job "${name}"?`)) {
        return;
    }

    try {
        await deleteJob(id);
        await loadJobs();
    } catch (err) {
        showError(err.message);
    }
}

// Payload Builder submit: POST /api/jobs to call SchedulePromptCron().
async function submitPayloadBuilder(e) {
    e.preventDefault();
    const status = document.getElementById('pb-status');
    status.textContent = 'Scheduling...';

    const cronExpr = document.getElementById('pb-cron-expr').value.trim();
    const jobId = document.getElementById('pb-job-id').value.trim();
    const rawPayload = document.getElementById('pb-payload').value.trim();

    let payload = null;
    if (rawPayload) {
        try {
            payload = JSON.parse(rawPayload);
        } catch (err) {
            status.className = 'form-status error';
            status.textContent = 'Payload is not valid JSON';
            return;
        }
    }

    try {
        await createScheduledJob(cronExpr, jobId, payload);
        status.className = 'form-status success';
        status.textContent = 'Job scheduled';
        await loadJobs();
    } catch (err) {
        status.className = 'form-status error';
        status.textContent = err.message;
    }
}

// Auto-Approve Security Center submit: POST /api/policy (action add).
async function submitPolicy(e) {
    e.preventDefault();
    const status = document.getElementById('policy-status');
    status.textContent = 'Saving...';

    const connectionId = document.getElementById('policy-connection-id').value.trim();
    if (!connectionId) {
        status.className = 'form-status error';
        status.textContent = 'Connection ID is required';
        return;
    }

    try {
        await updatePolicy(connectionId, 'add');
        status.className = 'form-status success';
        status.textContent = 'Connection allowed';
        document.getElementById('policy-connection-id').value = '';
        await loadPolicy();
    } catch (err) {
        status.className = 'form-status error';
        status.textContent = err.message;
    }
}

// Load and render the auto-approve policy rule list.
async function loadPolicy() {
    const list = document.getElementById('policy-items');
    const empty = document.getElementById('policy-empty');
    try {
        const response = await fetch(`${getApiBase()}/policy`, { cache: 'no-store' });
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }
        const rules = await response.json();
        if (rules && rules.length > 0) {
            empty.classList.add('hidden');
            list.innerHTML = rules.map(rule => `
                <li class="policy-item">
                    <code>${escapeHtml(rule.connection_id)}</code>
                    <button class="btn btn-danger btn-sm" data-conn="${escapeHtml(rule.connection_id)}">Remove</button>
                </li>
            `).join('');
        } else {
            empty.classList.remove('hidden');
            list.innerHTML = '';
        }
    } catch (err) {
        empty.classList.remove('hidden');
        empty.textContent = 'Failed to load policy rules';
        list.innerHTML = '';
    }
}

// Remove a policy rule (action delete).
async function removePolicy(connectionId) {
    try {
        await updatePolicy(connectionId, 'delete');
        await loadPolicy();
    } catch (err) {
        const status = document.getElementById('policy-status');
        status.className = 'form-status error';
        status.textContent = err.message;
    }
}

// rendering
function renderJobs() {
    const container = document.getElementById('jobs-container');

    // update job count in info banner
    const jobCount = document.getElementById('job-count');
    if (jobCount) {
        jobCount.textContent = jobs ? jobs.length : 0;
    }

    // clean up expanded state for deleted jobs
    if (jobs && jobs.length > 0) {
        const currentJobIds = new Set(jobs.map(j => j.id));
        expandedSchedules.forEach(id => {
            if (!currentJobIds.has(id)) {
                expandedSchedules.delete(id);
            }
        });
    } else {
        // no jobs, clear all expanded state
        expandedSchedules.clear();
    }

    if (!jobs || jobs.length === 0) {
        container.innerHTML = `
            <div class="empty-state">
                <div class="empty-icon">⏰</div>
                <h2>No Jobs Running</h2>
                <p>Jobs are defined in your Go code. Once you start your application with scheduled jobs, they will appear here.</p>
                <div class="empty-hint">
                    💡 <strong>Tip:</strong> Create jobs programmatically via <code>SchedulePromptCron</code> to see them listed here.
                </div>
            </div>
        `;
        return;
    }

    container.innerHTML = `
        <div class="job-list">
            <h2 class="job-list-title">Scheduled Jobs (${jobs.length})</h2>
            <div class="job-grid">
                ${jobs.map(job => renderJobCard(job)).join('')}
            </div>
        </div>
    `;
}

function renderJobCard(job) {
    const nextRun = job.nextRun ? formatDateTime(job.nextRun) : 'Never';
    const lastRun = job.lastRun ? formatDateTime(job.lastRun) : 'Never';
    const timeUntil = job.nextRun ? getTimeUntil(job.nextRun) : '';

    return `
        <div class="job-card">
            <div class="job-card-header">
                <h3 class="job-name">${escapeHtml(job.name)}</h3>
                <div class="job-actions">
                    <button
                        class="btn btn-success btn-sm"
                        type="button"
                        data-action="run-job"
                        data-id="${escapeAttr(job.id)}"
                        title="Run now"
                    >
                        ▶️
                    </button>
                    <button
                        class="btn btn-danger btn-sm"
                        type="button"
                        data-action="delete-job"
                        data-id="${escapeAttr(job.id)}"
                        data-name="${escapeAttr(job.name)}"
                        title="Delete"
                    >
                        🗑️
                    </button>
                </div>
            </div>

            ${job.tags && job.tags.length > 0 ? `
                <div class="job-tags">
                    ${job.tags.map(tag => `<span class="tag">🏷️ ${escapeHtml(tag)}</span>`).join('')}
                </div>
            ` : ''}

            ${job.schedule ? `
                <div class="job-info-item job-schedule-item">
                    <span class="job-info-label">Schedule:</span>
                    <div class="job-info-value">
                        <span class="schedule-badge">${escapeHtml(job.schedule)}</span>
                        ${job.scheduleDetail ? `
                            <span class="schedule-detail">${escapeHtml(job.scheduleDetail)}</span>
                        ` : ''}
                    </div>
                </div>
            ` : ''}

            <div class="job-info">
                <div class="job-info-item">
                    <span class="job-info-label">Next Run:</span>
                    <span class="job-info-value">
                        ${nextRun}
                        ${timeUntil ? `<span class="time-until">⏱️ ${timeUntil}</span>` : ''}
                    </span>
                </div>
                <div class="job-info-item">
                    <span class="job-info-label">Last Run:</span>
                    <span class="job-info-value">${lastRun}</span>
                </div>
                <div class="job-info-item">
                    <span class="job-info-label">Job ID:</span>
                    <span class="job-info-value job-id">${job.id}</span>
                </div>
            </div>

            ${job.nextRuns && job.nextRuns.length > 0 ? `
                <div class="job-schedule">
                    <button class="schedule-toggle" type="button" data-action="toggle-schedule" data-id="${escapeAttr(job.id)}">
                        <span id="toggle-icon-${escapeAttr(job.id)}">${expandedSchedules.has(job.id) ? '🔽' : '▶️'}</span> Upcoming Runs
                    </button>
                    <div id="schedule-${escapeAttr(job.id)}" class="schedule-details${expandedSchedules.has(job.id) ? '' : ' hidden'}">
                        ${job.nextRuns.map(run => `
                            <div class="schedule-item">📌 ${formatDateTime(run)}</div>
                        `).join('')}
                    </div>
                </div>
            ` : ''}
        </div>
    `;
}

function toggleSchedule(jobId) {
    const details = document.getElementById(`schedule-${jobId}`);
    const icon = document.getElementById(`toggle-icon-${jobId}`);

    if (details.classList.contains('hidden')) {
        details.classList.remove('hidden');
        icon.textContent = '🔽';
        expandedSchedules.add(jobId); // remember this is expanded
    } else {
        details.classList.add('hidden');
        icon.textContent = '▶️';
        expandedSchedules.delete(jobId); // remember this is collapsed
    }
}

// utility functions
function formatDateTime(dateStr) {
    if (!dateStr) return 'Never';
    const date = new Date(dateStr);
    return date.toLocaleString();
}

function getTimeUntil(dateStr) {
    if (!dateStr) return '';
    const date = new Date(dateStr);
    const now = new Date();
    const diff = date.getTime() - now.getTime();

    if (diff < 0) return 'Overdue';

    const seconds = Math.floor(diff / 1000);
    const minutes = Math.floor(seconds / 60);
    const hours = Math.floor(minutes / 60);
    const days = Math.floor(hours / 24);

    if (days > 0) return `in ${days}d ${hours % 24}h`;
    if (hours > 0) return `in ${hours}h ${minutes % 60}m`;
    if (minutes > 0) return `in ${minutes}m`;
    return `in ${seconds}s`;
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function escapeAttr(text) {
    return escapeHtml(String(text)).replace(/"/g, '&quot;');
}
