class DashboardApp {
    constructor() {
        this.apiBase = '/api';
        this.updateInterval = 2000; // 2 seconds
        this.selectedClient = null;
        this.init();
    }

    async init() {
        // Check if user is authenticated
        const isAuthenticated = await this.checkSession();
        if (!isAuthenticated) {
            window.location.href = '/login.html';
            return;
        }

        this.setupEventListeners();
        this.updateDashboard();
        setInterval(() => this.updateDashboard(), this.updateInterval);
        
        // Update last update time
        setInterval(() => this.updateLastUpdate(), 1000);
    }

    async checkSession() {
        try {
            const response = await fetch(`${this.apiBase}/session`);
            return response.ok;
        } catch (error) {
            return false;
        }
    }

    setupEventListeners() {
        // Modal controls
        document.getElementById('closeModal').addEventListener('click', () => this.closeModal());
        document.getElementById('cancelBtn').addEventListener('click', () => this.closeModal());
        document.getElementById('addTunnelBtn').addEventListener('click', () => this.addTunnel());
        
        // Close modal on outside click
        document.getElementById('editModal').addEventListener('click', (e) => {
            if (e.target.id === 'editModal') {
                this.closeModal();
            }
        });

        // Enter key on port inputs
        document.getElementById('remotePort').addEventListener('keypress', (e) => {
            if (e.key === 'Enter') this.addTunnel();
        });
        document.getElementById('localPort').addEventListener('keypress', (e) => {
            if (e.key === 'Enter') this.addTunnel();
        });

        // Log level filter
        document.getElementById('logLevel').addEventListener('change', () => this.renderLogs());

        // Clear logs button
        document.getElementById('clearLogsBtn').addEventListener('click', () => this.clearLogs());
    }

    async updateDashboard() {
        try {
            const status = await this.fetchStatus();
            this.renderStats(status);
            this.renderClients(status);
            this.renderTunnels(status);
            await this.renderLogs();
        } catch (error) {
            console.error('Failed to update dashboard:', error);
            this.setServerStatus(false);
        }
    }

    async fetchStatus() {
        const response = await fetch(`${this.apiBase}/status`);
        if (!response.ok) throw new Error('Failed to fetch status');
        return await response.json();
    }

    renderStats(status) {
        const { clients = {}, tunnels = [] } = status;
        const clientCount = Object.keys(clients).length;
        
        let streamCount = 0;
        Object.values(clients).forEach(client => {
            if (client.streams) {
                streamCount += client.streams.length;
            }
        });

        document.getElementById('clientCount').textContent = clientCount;
        document.getElementById('tunnelCount').textContent = tunnels.length;
        document.getElementById('streamCount').textContent = streamCount;
        
        this.setServerStatus(true);
    }

    renderClients(status) {
        const { clients = {} } = status;
        const clientsList = document.getElementById('clientsList');

        if (Object.keys(clients).length === 0) {
            clientsList.innerHTML = '<div class="empty-state">No clients connected</div>';
            return;
        }

        clientsList.innerHTML = Object.entries(clients).map(([machineId, client]) => `
            <div class="client-card">
                <div class="client-header">
                    <div class="client-name">
                        <span class="client-status"></span>
                        ${this.escapeHtml(machineId)}
                    </div>
                    <div style="display: flex; gap: 1rem; align-items: center;">
                        <div style="font-size: 0.85rem; color: var(--secondary);">
                            ${client.streams ? client.streams.length : 0} active stream(s)
                        </div>
                        <button class="client-edit-btn" onclick="window.app.openModal('${machineId}')">Edit</button>
                    </div>
                </div>
                <div class="client-tunnels">
                    ${(client.tunnels || []).map(tunnel => `
                        <div class="tunnel-badge">
                            <div class="tunnel-remote">${tunnel.remote}</div>
                            <div class="tunnel-arrow">→</div>
                            <div class="tunnel-local">:${tunnel.local}</div>
                        </div>
                    `).join('')}
                </div>
            </div>
        `).join('');
    }

    renderTunnels(status) {
        const { tunnels = [] } = status;
        const tunnelsList = document.getElementById('tunnelsList');

        if (tunnels.length === 0) {
            tunnelsList.innerHTML = '<div class="empty-state">No tunnels configured</div>';
            return;
        }

        tunnelsList.innerHTML = tunnels.map(tunnel => `
            <div class="tunnel-row">
                <div class="tunnel-column">
                    <div class="tunnel-label">Server Port (Public)</div>
                    <div class="tunnel-value">${tunnel.remote}</div>
                </div>
                <div class="tunnel-arrow-large">→</div>
                <div class="tunnel-column">
                    <div class="tunnel-label">Service Port (Private)</div>
                    <div class="tunnel-value">${tunnel.local}</div>
                </div>
                <div class="tunnel-column">
                    <div class="tunnel-label">Client</div>
                    <div class="tunnel-client">${this.escapeHtml(tunnel.client)}</div>
                </div>
            </div>
        `).join('');
    }

    setServerStatus(online) {
        const statusBadge = document.getElementById('serverStatus');
        if (online) {
            statusBadge.classList.remove('offline');
            statusBadge.innerHTML = '<span class="status-indicator"></span>Server Online';
        } else {
            statusBadge.classList.add('offline');
            statusBadge.innerHTML = '<span class="status-indicator"></span>Server Offline';
        }
    }

    updateLastUpdate() {
        const now = new Date();
        const timeStr = now.toLocaleTimeString();
        document.getElementById('lastUpdate').textContent = timeStr;
    }

    escapeHtml(text) {
        const map = {
            '&': '&amp;',
            '<': '&lt;',
            '>': '&gt;',
            '"': '&quot;',
            "'": '&#039;'
        };
        return text.replace(/[&<>"']/g, m => map[m]);
    }

    openModal(clientId) {
        this.selectedClient = clientId;
        document.getElementById('editClientName').textContent = clientId;
        document.getElementById('remotePort').value = '';
        document.getElementById('localPort').value = '';
        
        // Load current tunnels
        this.loadClientTunnels(clientId);
        
        // Show modal
        document.getElementById('editModal').classList.add('show');
    }

    closeModal() {
        document.getElementById('editModal').classList.remove('show');
        this.selectedClient = null;
    }

    async loadClientTunnels(clientId) {
        try {
            const response = await fetch(`${this.apiBase}/client/${clientId}`);
            if (!response.ok) throw new Error('Failed to load client');
            
            const data = await response.json();
            const tunnelsList = document.getElementById('editTunnelsList');
            
            if (!data.tunnels || data.tunnels.length === 0) {
                tunnelsList.innerHTML = '<div class="empty-state">No tunnels configured</div>';
                return;
            }

            tunnelsList.innerHTML = data.tunnels.map(tunnel => `
                <div class="edit-tunnel-item">
                    <div class="edit-tunnel-mapping">
                        <span class="edit-tunnel-port">Server:${tunnel.remote}</span>
                        <span style="color: var(--secondary);">→</span>
                        <span style="color: var(--secondary);">Service:${tunnel.local}</span>
                    </div>
                    <div></div>
                    <button class="edit-tunnel-delete" onclick="window.app.removeTunnel('${clientId}', ${tunnel.remote})">Delete</button>
                </div>
            `).join('');
        } catch (error) {
            console.error('Failed to load client tunnels:', error);
            document.getElementById('editTunnelsList').innerHTML = '<div class="empty-state">Error loading tunnels</div>';
        }
    }

    async addTunnel() {
        const remotePort = parseInt(document.getElementById('remotePort').value);
        const localPort = parseInt(document.getElementById('localPort').value);

        if (!remotePort || !localPort) {
            this.showMessage('Please fill in all fields', 'error');
            return;
        }

        if (remotePort < 1 || remotePort > 65535 || localPort < 1 || localPort > 65535) {
            this.showMessage('Ports must be between 1 and 65535', 'error');
            return;
        }

        try {
            const response = await fetch(`${this.apiBase}/client/${this.selectedClient}/tunnel`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ remote: remotePort, local: localPort })
            });

            if (!response.ok) {
                const error = await response.json();
                throw new Error(error.error || 'Failed to add tunnel');
            }

            this.showMessage('Tunnel added successfully', 'success');
            document.getElementById('remotePort').value = '';
            document.getElementById('localPort').value = '';
            
            // Reload tunnels
            await this.loadClientTunnels(this.selectedClient);
            
            // Refresh main dashboard
            this.updateDashboard();
        } catch (error) {
            console.error('Failed to add tunnel:', error);
            this.showMessage('Error: ' + error.message, 'error');
        }
    }

    async removeTunnel(clientId, remotePort) {
        if (!confirm(`Remove tunnel on port ${remotePort}?`)) {
            return;
        }

        try {
            const response = await fetch(`${this.apiBase}/client/${clientId}/tunnel/${remotePort}`, {
                method: 'DELETE'
            });

            if (!response.ok) {
                const error = await response.json();
                throw new Error(error.error || 'Failed to remove tunnel');
            }

            this.showMessage('Tunnel removed successfully', 'success');
            
            // Reload tunnels
            await this.loadClientTunnels(clientId);
            
            // Refresh main dashboard
            this.updateDashboard();
        } catch (error) {
            console.error('Failed to remove tunnel:', error);
            this.showMessage('Error: ' + error.message, 'error');
        }
    }

    showMessage(text, type) {
        // Create a temporary message element
        const message = document.createElement('div');
        message.className = `message ${type} show`;
        message.textContent = text;
        
        const modal = document.querySelector('.modal-body');
        if (modal) {
            modal.insertBefore(message, modal.firstChild);
            setTimeout(() => message.remove(), 3000);
        }
    }

    async logout() {
        if (!confirm('Are you sure you want to logout?')) {
            return;
        }

        try {
            const response = await fetch(`${this.apiBase}/logout`, {
                method: 'POST'
            });

            if (response.ok) {
                localStorage.removeItem('session_token');
                window.location.href = '/login';
            } else {
                alert('Logout failed');
            }
        } catch (error) {
            console.error('Logout error:', error);
            alert('Logout error: ' + error.message);
        }
    }

    async renderLogs() {
        try {
            const level = document.getElementById('logLevel').value;
            const url = new URL(`${this.apiBase}/logs`, window.location.origin);
            url.searchParams.set('limit', '100');
            if (level) {
                url.searchParams.set('level', level);
            }

            const response = await fetch(url);
            if (!response.ok) throw new Error('Failed to fetch logs');
            
            const data = await response.json();
            const logsList = document.getElementById('logsList');

            if (!data.logs || data.logs.length === 0) {
                logsList.innerHTML = '<div class="empty-state">No logs available</div>';
                return;
            }

            logsList.innerHTML = data.logs.map(log => {
                const levelClass = log.level.toLowerCase();
                return `
                    <div class="log-entry ${levelClass}">
                        <div class="log-timestamp">${log.timestamp}</div>
                        <div class="log-level">${log.level}</div>
                        <div class="log-message">${this.escapeHtml(log.message)}</div>
                    </div>
                `;
            }).join('');
            // Don't auto-scroll - let user control viewing
        } catch (error) {
            console.error('Failed to fetch logs:', error);
            document.getElementById('logsList').innerHTML = '<div class="empty-state">Error loading logs</div>';
        }
    }

    async clearLogs() {
        if (!confirm('Are you sure you want to clear all logs?')) {
            return;
        }

        try {
            const response = await fetch(`${this.apiBase}/logs`, {
                method: 'DELETE'
            });

            if (!response.ok) throw new Error('Failed to clear logs');
            
            await this.renderLogs();
        } catch (error) {
            console.error('Failed to clear logs:', error);
            alert('Error clearing logs: ' + error.message);
        }
    }

    async renderLogs() {
        try {
            const level = document.getElementById('logLevel').value;
            const logsData = await this.getLogs(level, 100);
            const logsList = document.getElementById('logsList');

            if (!logsData.logs || logsData.logs.length === 0) {
                logsList.innerHTML = '<div class="empty-state">No logs available</div>';
                return;
            }

            logsList.innerHTML = logsData.logs.map(log => `
                <div class="log-entry ${log.level.toLowerCase()}">
                    <div class="log-timestamp">${log.timestamp}</div>
                    <div class="log-level">${log.level}</div>
                    <div class="log-message">${this.escapeHtml(log.message)}</div>
                </div>
            `).join('');

            // Don't auto-scroll - let user control viewing
        } catch (error) {
            console.error('Failed to render logs:', error);
        }
    }

    async getLogs(level = '', limit = 100) {
        const url = new URL(`${this.apiBase}/logs`, window.location.origin);
        if (level) url.searchParams.append('level', level);
        url.searchParams.append('limit', limit);

        const response = await fetch(url.toString());
        if (!response.ok) throw new Error('Failed to fetch logs');
        return await response.json();
    }

    async clearLogs() {
        if (!confirm('Are you sure you want to clear all logs?')) {
            return;
        }

        try {
            const response = await fetch(`${this.apiBase}/logs`, {
                method: 'DELETE'
            });

            if (response.ok) {
                await this.renderLogs();
                this.showMessage('Logs cleared', 'success');
            }
        } catch (error) {
            console.error('Failed to clear logs:', error);
            this.showMessage('Error clearing logs', 'error');
        }
    }
}

// Start app when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    window.app = new DashboardApp();
});
