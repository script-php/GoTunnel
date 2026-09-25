(() => {
    const storageKey = 'gotunnel-theme';
    const savedTheme = localStorage.getItem(storageKey);
    const initialTheme = savedTheme === 'light' ? 'light' : 'dark';
    document.documentElement.dataset.theme = initialTheme;

    function updateButton(button) {
        const isDark = document.documentElement.dataset.theme !== 'light';
        button.textContent = isDark ? '☀ Light' : '● Dark';
        button.setAttribute('aria-label', isDark ? 'Switch to light theme' : 'Switch to dark theme');
        button.setAttribute('title', isDark ? 'Switch to light theme' : 'Switch to dark theme');
    }

    document.addEventListener('DOMContentLoaded', () => {
        const button = document.getElementById('themeToggle');
        if (!button) return;
        updateButton(button);
        button.addEventListener('click', () => {
            const nextTheme = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
            document.documentElement.dataset.theme = nextTheme;
            localStorage.setItem(storageKey, nextTheme);
            updateButton(button);
        });
    });
})();
