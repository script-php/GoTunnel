class LoginApp {
    constructor() {
        this.form = document.getElementById('loginForm');
        this.passwordInput = document.getElementById('password');
        this.errorMessage = document.getElementById('errorMessage');
        
        this.form.addEventListener('submit', (e) => this.handleLogin(e));
    }

    handleLogin(e) {
        e.preventDefault();
        const password = this.passwordInput.value;

        if (!password) {
            this.showError('Password is required');
            return;
        }

        this.login(password);
    }

    login(password) {
        fetch('/api/login', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({
                password: password
            })
        })
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                this.showError(data.error);
                this.passwordInput.value = '';
            } else if (data.token) {
                // Login successful, redirect to dashboard
                localStorage.setItem('session_token', data.token);
                window.location.href = '/';
            }
        })
        .catch(error => {
            this.showError('Login failed: ' + error.message);
            this.passwordInput.value = '';
        });
    }

    showError(message) {
        this.errorMessage.textContent = message;
        this.errorMessage.style.display = 'block';
        setTimeout(() => {
            this.errorMessage.style.display = 'none';
        }, 5000);
    }
}

document.addEventListener('DOMContentLoaded', () => {
    new LoginApp();
});
