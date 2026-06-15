<#import "template.ftl" as layout>
<@layout.registrationLayout displayMessage=false; section>
    <#if section = "form">
        <h2>Error</h2>

        <div class="alert alert-error" style="margin: 16px 0;">
            <span>${kcSanitize(message.summary)?no_esc}</span>
        </div>

        <#if skipLink??>
        <#elseif client?? && client.baseUrl?has_content>
            <p style="text-align: center; margin-top: 20px;">
                <a href="${client.baseUrl}" style="display:inline-block;width:100%;background:linear-gradient(135deg,#1f2937,#111827);color:#fff;text-align:center;padding:14px 32px;border-radius:10px;font-weight:700;font-size:16px;text-decoration:none;">Back to Application</a>
            </p>
        </#if>
    </#if>
</@layout.registrationLayout>
