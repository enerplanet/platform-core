<!DOCTYPE html>
<html lang="en" xmlns="http://www.w3.org/1999/xhtml">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta http-equiv="X-UA-Compatible" content="IE=edge">
    <title>Password Reset - Storcito</title>
    <!--[if mso]>
    <style type="text/css">
        body, table, td { font-family: Arial, Helvetica, sans-serif !important; }
    </style>
    <![endif]-->
</head>
<body style="margin: 0; padding: 0; background-color: #f3f4f6; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; -webkit-font-smoothing: antialiased;">
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color: #f3f4f6;">
        <tr>
            <td align="center" style="padding: 40px 16px;">
                <table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="max-width: 600px; width: 100%; background-color: #ffffff; border-radius: 16px; overflow: hidden; box-shadow: 0 4px 24px rgba(0, 0, 0, 0.08);">

                    <!-- Header -->
                    <tr>
                        <td style="background: linear-gradient(135deg, #1f2937 0%, #111827 100%); padding: 36px 32px; text-align: center;">
                            <table role="presentation" cellpadding="0" cellspacing="0" border="0" align="center">
                                <tr>
                                    <td style="padding-bottom: 12px;">
                                        <div style="width: 56px; height: 56px; margin: 0 auto; background: rgba(255,255,255,0.1); border-radius: 14px; line-height: 56px; text-align: center;">
                                            <span style="font-size: 28px; color: #ffffff;">&#9889;</span>
                                        </div>
                                    </td>
                                </tr>
                            </table>
                            <h1 style="margin: 0; font-size: 28px; font-weight: 800; color: #ffffff; letter-spacing: 0.5px; text-transform: uppercase;">Storcito</h1>
                            <p style="margin: 6px 0 0; font-size: 13px; color: rgba(255,255,255,0.5); font-weight: 400; letter-spacing: 0.3px;">Energy Simulation Platform</p>
                        </td>
                    </tr>

                    <!-- Accent bar -->
                    <tr>
                        <td style="height: 3px; background: linear-gradient(90deg, #374151, #6b7280, #9ca3af, #6b7280, #374151);"></td>
                    </tr>

                    <!-- Body -->
                    <tr>
                        <td style="padding: 44px 36px 20px;">
                            <p style="margin: 0 0 8px; font-size: 15px; color: #6b7280;">Hello,</p>
                            <h2 style="margin: 0 0 24px; font-size: 22px; font-weight: 700; color: #111827;">${user.attributes.fullName[0]!user.email}</h2>

                            <p style="margin: 0 0 20px; font-size: 15px; color: #374151; line-height: 1.6;">
                                We received a request to reset your password for your Storcito account. If you didn't make this request, you can safely ignore this email.
                            </p>

                            <!-- Security notice -->
                            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
                                <tr>
                                    <td style="background-color: #f9fafb; border: 1px solid #e5e7eb; border-radius: 12px; padding: 20px 24px;">
                                        <p style="margin: 0; font-size: 14px; color: #4b5563; line-height: 1.6;">
                                            <strong style="color: #1f2937;">&#128274; Security Notice:</strong><br>
                                            This password reset link will expire in <strong>${linkExpirationFormatter(linkExpiration)}</strong>. For your security, make sure to use a strong, unique password.
                                        </p>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>

                    <!-- CTA Button -->
                    <tr>
                        <td align="center" style="padding: 16px 36px 36px;">
                            <table role="presentation" cellpadding="0" cellspacing="0" border="0">
                                <tr>
                                    <td align="center" style="background: linear-gradient(135deg, #1f2937 0%, #111827 100%); border-radius: 10px;">
                                        <a href="${link}" target="_blank" style="display: inline-block; padding: 16px 48px; font-size: 16px; font-weight: 700; color: #ffffff; text-decoration: none; letter-spacing: 0.3px;">
                                            Reset Your Password
                                        </a>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>

                    <!-- Divider -->
                    <tr>
                        <td style="padding: 0 36px;">
                            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
                                <tr>
                                    <td style="border-top: 1px solid #e5e7eb; height: 1px; font-size: 0; line-height: 0;">&nbsp;</td>
                                </tr>
                            </table>
                        </td>
                    </tr>

                    <!-- Fallback link -->
                    <tr>
                        <td style="padding: 24px 36px 12px;">
                            <p style="margin: 0 0 10px; font-size: 13px; color: #9ca3af;">If the button doesn't work, copy and paste this link into your browser:</p>
                            <p style="margin: 0; background-color: #f9fafb; border: 1px solid #e5e7eb; border-radius: 8px; padding: 12px 16px; word-break: break-all; font-family: 'SF Mono', 'Fira Code', Consolas, monospace; font-size: 11px; color: #6b7280; line-height: 1.5;">${link}</p>
                        </td>
                    </tr>

                    <!-- Didn't request notice -->
                    <tr>
                        <td style="padding: 16px 36px 36px;">
                            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
                                <tr>
                                    <td style="background-color: #f9fafb; border: 1px solid #e5e7eb; border-radius: 8px; padding: 12px 16px;">
                                        <p style="margin: 0; font-size: 13px; color: #4b5563; line-height: 1.5;">
                                            <strong>Didn't request this?</strong> If you didn't request a password reset, please ignore this email. Your account remains secure.
                                        </p>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>

                    <!-- Footer -->
                    <tr>
                        <td style="background-color: #111827; padding: 32px 36px; text-align: center;">
                            <p style="margin: 0 0 6px; font-size: 16px; font-weight: 700; color: #ffffff; text-transform: uppercase; letter-spacing: 0.5px;">Storcito</p>
                            <p style="margin: 0 0 4px; font-size: 12px; color: #d1d5db; line-height: 1.6;">Advanced energy simulation and analysis</p>
                            <p style="margin: 0 0 12px; font-size: 12px; color: #d1d5db; line-height: 1.6;">Empowering sustainable energy decisions</p>
                            <p style="margin: 0 0 8px; font-size: 11px; color: #e5e7eb;">Technische Hochschule Deggendorf</p>
                            <p style="margin: 0; font-size: 11px; color: #d1d5db;">&copy; 2026 Storcito. All rights reserved.</p>
                        </td>
                    </tr>

                </table>

                <!-- Below card note -->
                <table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="max-width: 600px; width: 100%;">
                    <tr>
                        <td style="padding: 20px 0; text-align: center;">
                            <p style="margin: 0; font-size: 12px; color: #9ca3af; line-height: 1.5;">
                                This is an automated message from Storcito. Please do not reply to this email.
                            </p>
                        </td>
                    </tr>
                </table>
            </td>
        </tr>
    </table>
</body>
</html>
