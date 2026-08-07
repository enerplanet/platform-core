<#macro registrationLayout bodyClass="" displayInfo=false displayMessage=true displayRequiredFields=false>
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>${msg("loginTitle",(realm.displayName!'Storcito'))}</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            background: linear-gradient(135deg, #111827 0%, #1f2937 50%, #111827 100%);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 20px;
            -webkit-font-smoothing: antialiased;
        }
        .ep-container {
            width: 100%;
            max-width: 440px;
        }
        .ep-card {
            background: #ffffff;
            border-radius: 16px;
            box-shadow: 0 4px 24px rgba(0, 0, 0, 0.15);
            overflow: hidden;
        }
        .ep-header {
            background: linear-gradient(135deg, #1f2937 0%, #111827 100%);
            padding: 32px 24px;
            text-align: center;
        }
        .ep-icon {
            width: 56px;
            height: 56px;
            margin: 0 auto 12px;
            background: rgba(255,255,255,0.1);
            border-radius: 14px;
            line-height: 56px;
            text-align: center;
            font-size: 28px;
        }
        .ep-logo-img {
            height: 44px;
            width: auto;
            max-width: 85%;
            margin: 0 auto;
            display: block;
            /* Render the dark brand logo as white on the dark header */
            filter: brightness(0) invert(1);
        }
        .ep-logo {
            font-size: 28px;
            font-weight: 800;
            color: #ffffff;
            letter-spacing: 0.5px;
            text-transform: uppercase;
        }
        .ep-tagline {
            font-size: 13px;
            color: rgba(255,255,255,0.5);
            margin-top: 6px;
            letter-spacing: 0.3px;
        }
        .ep-accent {
            height: 3px;
            background: linear-gradient(90deg, #374151, #6b7280, #9ca3af, #6b7280, #374151);
        }
        .ep-body {
            padding: 36px 32px;
        }
        .ep-body h2 {
            color: #111827;
            font-size: 22px;
            font-weight: 700;
            margin-bottom: 8px;
        }
        .ep-body p {
            color: #6b7280;
            font-size: 14px;
            line-height: 1.6;
            margin-bottom: 20px;
        }
        /* Form styling */
        label {
            display: block;
            color: #374151;
            font-size: 14px;
            font-weight: 600;
            margin-bottom: 6px;
        }
        input[type="text"],
        input[type="password"],
        input[type="email"] {
            width: 100%;
            background-color: #f9fafb;
            border: 1px solid #e5e7eb;
            border-radius: 10px;
            padding: 12px 16px;
            font-size: 15px;
            color: #111827;
            outline: none;
            transition: border-color 0.2s, box-shadow 0.2s;
            margin-bottom: 16px;
            font-family: inherit;
        }
        input[type="text"]:focus,
        input[type="password"]:focus,
        input[type="email"]:focus {
            border-color: #6b7280;
            box-shadow: 0 0 0 3px rgba(107, 114, 128, 0.15);
        }
        .form-group { margin-bottom: 16px; }
        /* Buttons */
        input[type="submit"],
        button[type="submit"],
        .btn-primary {
            width: 100%;
            background: linear-gradient(135deg, #1f2937 0%, #111827 100%);
            border: none;
            border-radius: 10px;
            padding: 14px 32px;
            font-size: 16px;
            font-weight: 700;
            color: #ffffff;
            cursor: pointer;
            transition: opacity 0.2s, transform 0.2s;
            letter-spacing: 0.3px;
            font-family: inherit;
            margin-top: 8px;
        }
        input[type="submit"]:hover,
        button[type="submit"]:hover,
        .btn-primary:hover {
            opacity: 0.9;
            transform: translateY(-1px);
        }
        .btn-default, a.btn {
            display: inline-block;
            background: transparent;
            border: 1px solid #e5e7eb;
            border-radius: 10px;
            color: #374151;
            font-weight: 600;
            padding: 10px 20px;
            text-decoration: none;
            font-size: 14px;
            cursor: pointer;
            font-family: inherit;
        }
        /* Links */
        a { color: #4b5563; text-decoration: none; font-weight: 500; }
        a:hover { color: #111827; text-decoration: underline; }
        /* Alerts */
        .alert {
            border-radius: 10px;
            padding: 14px 18px;
            font-size: 14px;
            margin-bottom: 20px;
            line-height: 1.5;
        }
        .alert-success {
            background-color: #f0fdf4;
            color: #166534;
            border: 1px solid #bbf7d0;
        }
        .alert-warning {
            background-color: #fffbeb;
            color: #92400e;
            border: 1px solid #fde68a;
        }
        .alert-error, .pf-m-danger {
            background-color: #fef2f2;
            color: #991b1b;
            border: 1px solid #fecaca;
        }
        .alert-info {
            background-color: #f9fafb;
            color: #374151;
            border: 1px solid #e5e7eb;
        }
        .kc-feedback-text { font-size: 14px; }
        /* Info text */
        .ep-info {
            padding: 0 32px 24px;
            text-align: center;
        }
        .ep-info p {
            color: #6b7280;
            font-size: 13px;
        }
        .ep-info a {
            color: #374151;
            font-weight: 600;
        }
        /* Footer */
        .ep-footer {
            background-color: #111827;
            padding: 24px 32px;
            text-align: center;
        }
        .ep-footer-brand {
            font-size: 14px;
            font-weight: 700;
            color: #ffffff;
            text-transform: uppercase;
            letter-spacing: 0.5px;
            margin-bottom: 4px;
        }
        .ep-footer-text {
            font-size: 11px;
            color: #d1d5db;
            line-height: 1.6;
        }
        .ep-footer-copy {
            font-size: 10px;
            color: #d1d5db;
            margin-top: 8px;
        }
        /* Hide default Keycloak elements */
        #kc-logo-wrapper { display: none; }
        /* Required fields */
        .required { color: #dc2626; }
        span.required { font-size: 12px; margin-left: 2px; }
    </style>
</head>
<body>
    <div class="ep-container">
        <div class="ep-card">
            <!-- Header -->
            <div class="ep-header">
                <img class="ep-logo-img" src="${url.resourcesPath}/img/logo.png" alt="${realm.displayName!'Storcito'}"/>
                <div class="ep-tagline">Energy Simulation Platform</div>
            </div>
            <div class="ep-accent"></div>

            <!-- Body -->
            <div class="ep-body">
                <#if displayMessage && message?has_content && (message.type != 'warning' || !isAppInitiatedAction??)>
                    <div class="alert alert-${message.type}">
                        <span class="kc-feedback-text">${kcSanitize(message.summary)?no_esc}</span>
                    </div>
                </#if>

                <#nested "form">
            </div>

            <#if displayInfo>
                <div class="ep-info">
                    <#nested "info">
                </div>
            </#if>

            <!-- Footer -->
            <div class="ep-footer">
                <div class="ep-footer-brand">Storcito</div>
                <div class="ep-footer-text">Technische Hochschule Deggendorf</div>
                <div class="ep-footer-copy">&copy; 2026 Storcito. All rights reserved.</div>
            </div>
        </div>
    </div>
</body>
</html>
</#macro>
